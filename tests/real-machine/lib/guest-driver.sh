# shellcheck shell=bash
# =============================================================================
# guest-driver.sh —— 一次性 verify 镜像 → qcow2 → QEMU guest → SSH 的共享驱动
# =============================================================================
# 解决什么问题
#   「只有真 guest 才能证伪」的探针(沙箱反证 / audit 真 inode / 破坏性插件 /
#   写守卫 / 只读插件 / 插件盘点 / copilot / bootc install+upgrade)每条都要
#   同一套准备动作:派生一个**带登录凭据的一次性镜像** → 出 qcow2 → 起 QEMU →
#   等 SSH。这套动作在本文件里只实现一次,探针只写自己的断言。
#
# 为什么需要派生镜像
#   发布镜像没有登录凭据(78-dev-user.sh 是凭据门控的,CI 不传),而探针必须在
#   guest 内执行。派生镜像复刻 78-dev-user.sh 的组/用户/sudoers/tmpfiles,只在
#   本地存在:不推 registry、不进发布产物、口令随机且用完即弃(证据只记指纹)。
#
# 用法(在 x86-64-v3 + KVM 宿主上,需 rootful podman 与 root)
#   source tests/real-machine/lib/guest-driver.sh
#   driver_init                 # 幂等:工作目录已有 qcow2 就复用
#   driver_boot                 # 起 QEMU 并等 SSH;导出 DRIVER_QEMU_PID
#   gssh uname -r               # 在 guest 里执行
#   gssh_scp /local/path /remote/path
#   driver_verdict PASS "A3-1 ..."
#   driver_exit                 # 累积语义:任一条 FAIL 即非零
#
# 环境变量
#   VERIFY_IMAGE        基准镜像(默认 ghcr.io/daedalusys/daedalus-os:latest)
#   VERIFY_WORK         工作目录(默认:调用者家目录下 daedalus-guest-driver)
#   VERIFY_MEM          引导内存 MiB(默认 4096)
#   VERIFY_BOOT_TIMEOUT 等 SSH/重启的单步上限秒数(默认 240)
#   VERIFY_SSH_PORT     hostfwd 的宿主端口(默认 2223;避开 iso-verify.sh 的 2222)
#   DRIVER_QEMU_SNAPSHOT    1 时引导写入落临时 overlay(基础 qcow2 不被写脏)
#   DRIVER_QEMU_EXTRA_ARGS  追加到 qemu 命令行的原始参数串(如再挂一块盘)
# =============================================================================

# 指纹工具与被测镜像绑定:复用 qcow2 前必须先确认它是由**当前**镜像构建的。
# 库不在时报「取不到指纹」会把人引向镜像问题,所以先显式判死。
_fp_lib="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/artifact-fingerprint.sh"
[ -r "$_fp_lib" ] || {
    echo "缺少指纹工具库: $_fp_lib(本库必须与 guest-driver.sh 同目录)" >&2
    exit 2
}
# shellcheck source=artifact-fingerprint.sh
. "$_fp_lib"

# 组/用户/口令:与 78-dev-user.sh 的要点一致(见下方 driver_derive 注释)。
DRIVER_USER="verify"

# 工作目录与日志:证据要能被**调用者**读走(CI upload-artifact 以 runner 用户读),
# 而本驱动以 root 跑,故把工作目录归还给发起 sudo 的用户。
_inv="${SUDO_USER:-$USER}"
_inv_home="$(getent passwd "$_inv" 2>/dev/null | cut -d: -f6)"; [ -n "${_inv_home:-}" ] || _inv_home="$HOME"
DRIVER_WORK="${VERIFY_WORK:-$_inv_home/daedalus-guest-driver}"
mkdir -p "$DRIVER_WORK" || { echo "无法创建工作目录 $DRIVER_WORK" >&2; exit 2; }
_owner="$_inv:$(id -gn "$_inv" 2>/dev/null || echo "$_inv")"
chown -R "$_owner" "$DRIVER_WORK" 2>/dev/null || true

DRIVER_LOG="${DRIVER_LOG:-$DRIVER_WORK/guest-driver.log}"
: > "$DRIVER_LOG"

log()  { printf '%s\n' "$*" >>"$DRIVER_LOG"; }
say()  { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; log ""; log "== $* =="; }
info() { printf '   %s\n' "$*"; log "   $*"; }
ok()   { printf '\033[1;32m   OK %s\033[0m\n' "$*"; log "   OK $*"; }
warn() { printf '\033[1;33m   !! %s\033[0m\n' "$*"; log "   !! $*"; }
bad()  { printf '\033[1;31m   XX %s\033[0m\n' "$*" >&2; log "   XX $*"; }
die()  { bad "$*"; driver_cleanup; exit 1; }

DRIVER_SSH_PORT="${VERIFY_SSH_PORT:-2223}"
DRIVER_MEM="${VERIFY_MEM:-4096}"
DRIVER_BOOT_TIMEOUT="${VERIFY_BOOT_TIMEOUT:-240}"
DRIVER_IMAGE="${VERIFY_IMAGE:-ghcr.io/daedalusys/daedalus-os:latest}"

DRIVER_QEMU_PID=""
DRIVER_FAIL_N=0
DRIVER_BOOTED=0

driver_cleanup() {
    if [ -n "${DRIVER_QEMU_PID:-}" ]; then
        kill "$DRIVER_QEMU_PID" 2>/dev/null
        sleep 1
        kill -9 "$DRIVER_QEMU_PID" 2>/dev/null
        DRIVER_QEMU_PID=""
    fi
    # 归还工作目录给调用者(证据要能被非 root 读走;失败路径也要还)。
    chown -R "${_owner:-root}" "$DRIVER_WORK" 2>/dev/null || true
}

# derive <from-ref> <new-tag> <extra-containerfile-lines> —— 派生一次性 verify 镜像。
# 复刻 78-dev-user.sh 的要点:video/render 组病态半初始化必须先补 /etc/group
# (否则 useradd -G 直接失败);home 必须走 tmpfiles(bootc 不从镜像 seed /var)。
driver_derive() {
    local from="$1" tag="$2" extra="$3"
    cat > "$DRIVER_WORK/Containerfile" <<EOF
FROM $from
RUN set -eux; \\
    grep -qE '^video:'  /etc/group || echo 'video:x:39:'   >> /etc/group; \\
    grep -qE '^render:' /etc/group || echo 'render:x:105:' >> /etc/group; \\
    (id ${DRIVER_USER} >/dev/null 2>&1 || useradd -m -G wheel,video,render -s /bin/bash ${DRIVER_USER}); \\
    echo '${DRIVER_USER}:${DRIVER_VPASS}' | chpasswd; \\
    printf '${DRIVER_USER} ALL=(ALL) NOPASSWD:ALL\n' > /etc/sudoers.d/${DRIVER_USER}; chmod 0440 /etc/sudoers.d/${DRIVER_USER}; \\
    printf 'd /var/home 0755 root root -\nd /var/home/${DRIVER_USER} 0700 ${DRIVER_USER} ${DRIVER_USER} -\n' > /etc/tmpfiles.d/${DRIVER_USER}.conf; \\
    systemctl enable sshd${extra:+; $extra}
EOF
    podman build --security-opt label=disable -t "$tag" -f "$DRIVER_WORK/Containerfile" "$DRIVER_WORK" >>"$DRIVER_LOG" 2>&1 \
        || { bad "派生镜像 $tag 构建失败,见 $DRIVER_LOG"; driver_cleanup; exit 1; }
}

# driver_init —— 幂等:qcow2 与派生镜像已就绪**且由当前被测镜像构建**时复用
# (重跑探针时省掉 ~7GB 构建)。
# 导出:DRIVER_QCOW2 / DRIVER_IMAGE_BASE
driver_init() {
    local vm="$DRIVER_WORK/verify-image"
    local out="$DRIVER_WORK/output"
    DRIVER_PASS_FILE="$DRIVER_WORK/.verify-pass"
    DRIVER_IMAGE_BASE="localhost/daedalus-verify:base"
    DRIVER_IMAGE_TARGET="localhost/daedalus-verify:switch-target"
    DRIVER_SERIAL="$DRIVER_WORK/serial-console.log"
    : > "$DRIVER_SERIAL"
    DRIVER_QCOW2="$(find "$out/qcow2" -name '*.qcow2' 2>/dev/null | head -1)"

    # 复用判据必须包含「产物与当前被测镜像同源」:只看文件在不在,会让上一次
    # run 留下的旧 qcow2 冒充本次镜像的产物,门禁结论因此失真。
    local fp_file="$DRIVER_WORK/.qcow2-fingerprint" fp_now
    if fp_now="$(artifact_fingerprint "$DRIVER_IMAGE")"; then :; else
        bad "取不到被测镜像 $DRIVER_IMAGE 的指纹"; exit 1
    fi
    if [ "${VERIFY_FORCE_REBUILD:-0}" = "1" ]; then
        say "VERIFY_FORCE_REBUILD=1 → 丢弃旧产物重建"
        rm -f "$DRIVER_QCOW2" 2>/dev/null || true
        podman rmi -f "$DRIVER_IMAGE_BASE" >/dev/null 2>&1 || true
        DRIVER_QCOW2=""
    elif artifact_fp_match "$fp_file" "$fp_now" \
         && [ -s "${DRIVER_QCOW2:-}" ] && [ -s "$DRIVER_PASS_FILE" ] \
         && podman image exists "$DRIVER_IMAGE_BASE"; then
        ok "复用已构建产物(qcow2 与被测镜像指纹一致: $fp_now)"
        DRIVER_VPASS="$(cat "$DRIVER_PASS_FILE")"
        return 0
    else
        say "产物指纹不一致 → 重建。旧=$(artifact_fp_describe "$fp_file") 新=$fp_now"
        rm -f "$DRIVER_QCOW2" 2>/dev/null || true
        podman rmi -f "$DRIVER_IMAGE_BASE" >/dev/null 2>&1 || true
        DRIVER_QCOW2=""
    fi

    mkdir -p "$vm" "$out/qcow2"
    [ -s "$DRIVER_PASS_FILE" ] || { head -c12 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$DRIVER_PASS_FILE"; chmod 600 "$DRIVER_PASS_FILE"; }
    DRIVER_VPASS="$(cat "$DRIVER_PASS_FILE")"

    say "driver_init · 派生一次性 verify 镜像(本地,不推 registry)"
    podman image exists "$DRIVER_IMAGE" || podman pull "$DRIVER_IMAGE" >>"$DRIVER_LOG" 2>&1 || { bad "拉取 $DRIVER_IMAGE 失败"; exit 1; }
    driver_derive "$DRIVER_IMAGE" "$DRIVER_IMAGE_BASE" ""
    ok "派生镜像就绪: $DRIVER_IMAGE_BASE(verify 用户: $DRIVER_USER,密码随机)"

    say "driver_init · 出 qcow2(从 base 派生镜像)"
    rm -f "$out/qcow2"/*.qcow2
    podman run --rm --privileged --security-opt label=disable \
        -v "$out/qcow2:/output" \
        -v /var/lib/containers/storage:/var/lib/containers/storage \
        quay.io/centos-bootc/bootc-image-builder:latest \
        --local --type qcow2 --output /output "$DRIVER_IMAGE_BASE" >>"$DRIVER_LOG" 2>&1 \
        || { bad "bootc-image-builder 失败,见 $DRIVER_LOG"; exit 1; }
    # 产物必须是**这次** builder 出的那一个:目录里若还留着别的 .qcow2(删失败的
    # 旧产物、builder 自己多产出的),按名字取第一个就可能取到旧的,而下面会把
    # 本次镜像的指纹写到它头上 —— 那等于拿旧产物冒充本次结果。
    qcow2_count="$(find "$out/qcow2" -name '*.qcow2' -type f | wc -l | tr -d ' ')"
    [ "$qcow2_count" = 1 ] || { bad "产物目录里有 $qcow2_count 个 .qcow2,期望恰好 1 个(见 $DRIVER_LOG)"; exit 1; }
    DRIVER_QCOW2="$(find "$out/qcow2" -name '*.qcow2' -type f | head -1)"
    [ -n "${DRIVER_QCOW2:-}" ] || { bad "builder 没产出 qcow2"; exit 1; }
    [ -s "$DRIVER_QCOW2" ] || { bad "builder 产出的 qcow2 是空文件: $DRIVER_QCOW2"; exit 1; }
    ok "qcow2: $DRIVER_QCOW2($(du -h "$DRIVER_QCOW2" | cut -f1))"
    # 指纹最后写:中途失败不会留下「产物在但没建完」的指纹。
    artifact_fp_write "$fp_file" "$fp_now"
}

# driver_oci_archive <tag> <out-path> —— 把镜像打成 OCI 归档(scp 进 guest 用)。
driver_oci_archive() {
    local tag="$1" path="$2"
    podman save --format oci-archive -o "$path" "$tag" >>"$DRIVER_LOG" 2>&1 \
        || { bad "podman save $tag 失败"; exit 1; }
    ok "OCI 归档: $path($(du -h "$path" | cut -f1))"
}

# driver_boot —— 起 QEMU(可写,允许重启),等 SSH 可达。导出 DRIVER_QEMU_PID。
# DRIVER_QEMU_SNAPSHOT=1 时写入落临时 overlay:基础 qcow2 不被写脏,多个探针
# 可安全复用同一份 qcow2(overlay 在重启间保持,故 boot/reboot 序列照常)。
driver_boot() {
    # 上一个探针若硬退出可能留下占着同端口的 qemu;只清我们自己的端口。
    pkill -f "hostfwd=tcp::${DRIVER_SSH_PORT}-:22" 2>/dev/null && sleep 1
    local drive_opts="if=virtio,format=qcow2"
    [ "${DRIVER_QEMU_SNAPSHOT:-0}" = 1 ] && drive_opts="$drive_opts,snapshot=on"
    qemu-system-x86_64 -machine q35,accel=kvm -cpu host -m "$DRIVER_MEM" -smp 2 \
        -nographic -drive file="$DRIVER_QCOW2",$drive_opts \
        -netdev user,id=net0,hostfwd=tcp::${DRIVER_SSH_PORT}-:22 -device virtio-net,netdev=net0 \
        ${DRIVER_QEMU_EXTRA_ARGS:-} \
        </dev/null >"$DRIVER_SERIAL" 2>&1 &
    DRIVER_QEMU_PID=$!
    info "qemu pid=$DRIVER_QEMU_PID,串口 → $DRIVER_SERIAL"

    cat > "$DRIVER_WORK/askpass.sh" <<EOF
#!/bin/sh
cat "$DRIVER_PASS_FILE"
EOF
    chmod 700 "$DRIVER_WORK/askpass.sh"
    export SSH_ASKPASS="$DRIVER_WORK/askpass.sh" SSH_ASKPASS_REQUIRE=force DISPLAY=:0
    DRIVER_SSH_OPTS=(-p "$DRIVER_SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
                     -o ConnectTimeout=8 -o LogLevel=ERROR -o PreferredAuthentications=password -o PubkeyAuthentication=no)
    if ! driver_wait_ssh "$DRIVER_BOOT_TIMEOUT"; then
        bad "等 SSH 超时;串口尾部:"
        tail -20 "$DRIVER_SERIAL" | sed 's/^/      /'
        driver_cleanup; exit 1
    fi
    DRIVER_BOOTED=1
    ok "SSH 可达(${DRIVER_USER}@127.0.0.1:${DRIVER_SSH_PORT})"
}

# gssh <cmd...> —— 在 guest 里执行(gssh -o ... 'cmd',或 gssh cmd1 '&&' cmd2)。
gssh() { ssh "${DRIVER_SSH_OPTS[@]}" "${DRIVER_USER}@127.0.0.1" "$@" </dev/null; }

# gssh_scp <local> <remote> —— 把文件送进 guest。
gssh_scp() {
    scp -P "$DRIVER_SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
        "$1" "${DRIVER_USER}@127.0.0.1:$2" >>"$DRIVER_LOG" 2>&1
}

# driver_wait_ssh <秒数> —— 轮询等 SSH 可达。
driver_wait_ssh() {
    local t="$1" i=0
    while [ "$i" -lt "$t" ]; do
        gssh true >/dev/null 2>&1 && return 0
        sleep 3; i=$((i+3))
    done
    return 1
}

# driver_reboot_and_wait <旧 boot_id> <秒数> —— 等 guest 重启后 SSH 回来且 boot_id 变化,
# 成功时把新 boot_id 打到 stdout。
driver_reboot_and_wait() {
    local old="$1" t="$2" i=0 b
    while [ "$i" -lt "$t" ]; do
        if gssh true >/dev/null 2>&1; then
            b="$(gssh 'cat /proc/sys/kernel/random/boot_id' 2>/dev/null | tr -d '\r')"
            [ -n "$b" ] && [ "$b" != "$old" ] && { printf '%s' "$b"; return 0; }
        fi
        sleep 3; i=$((i+3))
    done
    return 1
}

# driver_verdict <PASS|FAIL> <说明> —— 结论累积:任一 FAIL 即 driver_exit 非零。
driver_verdict() {
    local v="$1"; shift
    printf '%s  %s\n' "$v" "$*"
    log "$v $*"
    [ "$v" = FAIL ] && DRIVER_FAIL_N=$((DRIVER_FAIL_N + 1))
    return 0
}

# driver_exit —— 统一收尾:清理 QEMU、归还工作目录、按累积结果给退出码。
driver_exit() {
    driver_cleanup
    if [ "$DRIVER_FAIL_N" -gt 0 ]; then
        printf '\n判定: FAIL(%d 条)\n' "$DRIVER_FAIL_N"
        exit 1
    fi
    printf '\n判定: PASS\n'
    exit 0
}
