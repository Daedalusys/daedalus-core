#!/usr/bin/env bash
# =============================================================================
# probe-bootc-install-upgrade.sh —— A2:bootc install to-disk 与 bootc upgrade
# =============================================================================
# 证伪什么
#   P2-1 已覆盖「换部署 / 回滚」(bootc switch / rollback)。但 `bootc install
#   to-disk` 与 `bootc upgrade` 从未被单独验过 —— bootc-image-builder 出的 qcow2
#   内部做过 install,那不等于「在 guest 里手工跑 install」。
#
# 两条
#   1. `bootc install to-disk` 对**第二块虚拟盘**(/dev/vdb)执行,成功后目标盘上
#      出现 ostree 部署(挂载后 ostree admin status 可见)。**绝不指向宿主盘**;
#      判据两条同时成立才算过:rc=0 **且** 目标盘根分区上有部署 —— 只判 rc 会把
#      「装了个空壳」记成成功。
#   2. `bootc upgrade`:同一 ref 的两个 digest(不是 switch 语义)。
#
# 校准史(为什么不再死磕「四种 transport 写法」)
#   2026-11-07 版把同一份 OCI 归档按 oci-archive: / oci: / registry: /
#   docker:// 四种写法各试一次,一律停在 `Creating source info from a given
#   imageref`,判据只能落 PROBE_WIP —— 分不清「调用姿势不对」与「bootc 对这类
#   镜像有缺陷」。那些写法是**同一条路径的四种拼写**:都要把源镜像重新导入成
#   一个 ostree commit。
#   2026-11-17 校准(issue #105)换的是**结构性**差异,不是又一种拼写:
#     候选 #1  privileged 容器内跑 bootc —— bootc 自己的 --help 要求的形态
#              ("This command must be invoked inside of the container, which
#               will be installed" + "--privileged"),镜像的 /usr 就是安装源;
#     候选 #2  containers-storage:<guest 内 root podman storage 的实际 ref>
#     候选 #3  registry:127.0.0.1:5000/... —— guest 内本地 registry(http/insecure)
#   候选 #2/#3 在 1.16.13 上与四种远程写法停在同一行(子进程逐字:
#   `error: Multiple commit objects found`)。保留它们只为**一次取全原始错误**,
#   不是继续在候选列表里打转:候选 #1 成功就不再往下试。
#
# 用法: sudo VERIFY_WORK=/tmp/probe-a2 bash tests/real-machine/probe-bootc-install-upgrade.sh
# =============================================================================
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export VERIFY_WORK="${VERIFY_WORK:-/tmp/daedalus-probe-suite}"
export DRIVER_LOG="$VERIFY_WORK/probe-bootc-install-upgrade.log"
# shellcheck source=lib/guest-driver.sh
source "$HERE/lib/guest-driver.sh"

driver_init
# 第二块虚拟盘:install to-disk 的唯一目标。它是**宿主上的一个文件**,不是宿主盘。
# 每次重跑都重建:上一次失败的尝试会在盘上留下半成品 ostree 仓库。
VDB="$DRIVER_WORK/a2-vdb.qcow2"
rm -f "$VDB"
qemu-img create -f qcow2 "$VDB" 20G >/dev/null
export DRIVER_QEMU_EXTRA_ARGS="-drive file=$VDB,if=virtio,format=qcow2"
DRIVER_QEMU_SNAPSHOT=1
# guest 里要同时装下 2.2GB 源镜像(podman storage)与本地 registry 的副本,
# 4G 会把 OOM 与回写拖死;8G 与宿主 15G 内存仍有余量。
DRIVER_MEM="${VERIFY_MEM:-8192}"

# A2-3 的口径:比对**块设备结构与文件系统身份**(NAME/SIZE/TYPE/FSTYPE/UUID),
# 不比对 MOUNTPOINT。宿主用 btrfs 子卷(`/var/lib/containers/storage/overlay` 是
# 常驻挂载),同一块 nvme 同时挂着 / 与 /home;podman 在 storage 树里做挂载/卸载会
# 让 lsblk 的 MOUNTPOINT 列在「选哪个挂载点打印」上抖动 —— 那是挂载动作,不是
# 「往宿主盘里写」。改成 UUID 口径后:真的重分区/重格式化会立刻被抓到(NAME/SIZE/
# TYPE/FSTYPE/UUID 全变),而挂载抖动不再误报。
HOST_LSBLK_CMD=(lsblk -o NAME,SIZE,TYPE,FSTYPE,UUID)
HOST_LSBLK_BEFORE="$("${HOST_LSBLK_CMD[@]}" 2>&1)"
driver_boot

# ---- 常量与 guest 侧助手 --------------------------------------------------
# 所有 podman 动作都以 root 跑:bootc install 也在 root 下,两者必须看同一个
# storage。rootless 的 ~/.local/share/containers/storage 对 bootc 不可见 ——
# 「load 成功但 bootc 说 ref 不存在」就是这么来的(看起来像「transport 不支持」)。
REG_PORT=5000
REG_NAME=a2-registry
REG_VOL=a2-registry-data
REG_REPO=daedalus-verify
REG_HOST="127.0.0.1:$REG_PORT"
# registry 镜像的候选源:从宿主送进 guest,不让 guest 走外网(QEMU user-mode NAT)。
REG_IMAGE_CANDIDATES=(
    "public.ecr.aws/docker/library/registry:2"
    "docker.io/library/registry:2"
)

# bootc_json <subcmd...> —— guest 里 bootc 的 JSON 输出(1.16 起有 --format json)。
bootc_json() { gssh "sudo bootc $* --format json 2>/dev/null" 2>&1; }

# json_get <json> <点路径> —— 取 JSON 里的一个字符串字段;取不到给空串。
# 不用 jq:guest 里不保证有;python3 是 AlmaLinux 基础镜像的标配,没有就退 grep。
json_get() {
    local json="$1" path="$2" val=""
    if printf '%s' "$json" | grep -q '{'; then
        val="$(printf '%s' "$json" | python3 -c '
import json,sys
d=json.load(sys.stdin)
for k in sys.argv[1].split("."):
    d=(d or {}).get(k) if isinstance(d,dict) else None
print(d if isinstance(d,str) else "")' "$path" 2>/dev/null)"
    fi
    [ -n "$val" ] || val="$(printf '%s' "$json" | sed -n "s/.*\"${path##*.}\": *\"\([^\"]*\)\".*/\1/p" | head -1)"
    printf '%s' "$val"
}

say "A2-0 · guest 内盘位 / bootc 版本 / podman 与存储"
# --help 原文进日志:判据的每一条都要能对着 bootc 自己的文档核。
gssh "lsblk -o NAME,SIZE,TYPE; echo '--- bootc ---'; bootc --version; echo '--- status ---'; sudo bootc status 2>&1 | head -12; echo '--- podman ---'; sudo podman --version; sudo podman info --format 'storage_driver={{.Store.GraphDriverName}} graph_root={{.Store.GraphRoot}}'; echo '--- disk ---'; df -h / /var /sysroot 2>/dev/null; echo '--- ostree prepare-root ---'; ls -l /usr/lib/ostree/prepare-root.conf 2>&1; echo '--- ostree deploy ---'; sudo ostree admin status 2>&1 | head -6; echo '--- repo commit 数 ---'; sudo ls /sysroot/ostree/repo/objects/*/*.commit 2>/dev/null | wc -l" 2>&1 | sed 's/^/      /' | tee -a "$DRIVER_LOG"

say "A2-1 · bootc install to-disk /dev/vdb"
gssh "sudo bootc install to-disk --help 2>&1 | sed -n '1,24p'" 2>&1 | sed 's/^/      /' | tee -a "$DRIVER_LOG"

# 源镜像:宿主侧打成 OCI 归档送进 guest,零凭据、零 guest 外网依赖。
ARCHIVE="$DRIVER_WORK/a2-source.tar"
driver_oci_archive "$DRIVER_IMAGE_BASE" "$ARCHIVE"
gssh_scp "$ARCHIVE" /home/verify/a2-source.tar || die "源镜像归档送进 guest 失败"
info "源镜像归档: $ARCHIVE → guest:/home/verify/a2-source.tar"

say "A2-1 · 源镜像 load 进 guest 的 root podman storage"
LOAD_OUT="$(gssh "sudo podman load -i /home/verify/a2-source.tar 2>&1 | tail -3; echo '---refs---'; sudo podman images --format '{{.Repository}}:{{.Tag}}'" 2>&1)"
printf '%s\n' "$LOAD_OUT" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
CS_REF="$(printf '%s\n' "$LOAD_OUT" | sed -n '/^---refs---$/,$p' | tail -n +2 | tr -d '\r' | grep -vE '^<none>' | head -1)"
# 归档在 guest 里已无用:guest 的根盘只有 8.4G。源镜像要**留到 A2-2 推完**
# (A2-2 的 B/C 是在 guest 里从这个镜像派生的)。
gssh "rm -f /home/verify/a2-source.tar" || true

# ---- guest 内本地 registry ------------------------------------------------
guest_registry_up() {
    local img="" c regtar
    for c in "${REG_IMAGE_CANDIDATES[@]}"; do
        gssh "sudo podman image exists '$c'" >/dev/null 2>&1 && { img="$c"; break; }
    done
    if [ -z "$img" ]; then
        for c in "${REG_IMAGE_CANDIDATES[@]}"; do
            regtar="$DRIVER_WORK/a2-registry-$(printf '%s' "$c" | tr '/:' '__').tar"
            if [ ! -s "$regtar" ]; then
                net_retry "$NET_SAVE_TIMEOUT" "$NET_SAVE_ATTEMPTS" \
                    podman save --format oci-archive -o "$regtar" "$c" >>"$DRIVER_LOG" 2>&1 || continue
            fi
            gssh_scp "$regtar" /home/verify/a2-registry.tar >/dev/null 2>&1 || continue
            gssh "sudo podman load -i /home/verify/a2-registry.tar" >/dev/null 2>&1 || continue
            gssh "sudo podman image exists '$c'" >/dev/null 2>&1 && { img="$c"; break; }
        done
    fi
    [ -n "$img" ] || { warn "guest 内拿不到 registry 镜像(候选: ${REG_IMAGE_CANDIDATES[*]})"; return 1; }
    REG_IMAGE="$img"
    # 数据放**命名卷**而不是 bind mount:bind mount 在 SELinux enforcing 下会被
    # 拒绝(registry 报 500 `mkdir /var/lib/registry/docker: permission denied`);
    # 命名卷由 podman 打标签,且落在 guest 自己的盘上 —— **重启后数据还在**,
    # 升级阶段不必重推一遍 A。容器本身随重启消失,所以要 rm 再 run。
    gssh "sudo podman rm -f $REG_NAME >/dev/null 2>&1; sudo podman run -d --name $REG_NAME -p $REG_PORT:$REG_PORT -v $REG_VOL:/var/lib/registry '$img'" >/dev/null 2>&1 || return 1
    local i=0
    while [ "$i" -lt 90 ]; do
        # registry:2 是 alpine,busybox wget 够用;不引入 guest 里的 curl 依赖。
        gssh "sudo podman exec $REG_NAME wget -q -O- http://127.0.0.1:$REG_PORT/v2/" >/dev/null 2>&1 && return 0
        sleep 2; i=$((i+2))
    done
    return 1
}

# guest_push <src-ref> <repo:tag> —— 在 guest 内打 tag 并推到本地 registry。
# http 回环必须 --tls-verify=false,否则会得到 TLS 握手错误(看起来像「镜像不存在」)。
# digest 用 --digestfile 落盘再读:podman push 不保证把 digest 打进日志。
guest_push() {
    local src="$1" dst="$2"
    gssh "sudo rm -f /tmp/a2-push.digest; sudo podman tag '$src' '$REG_HOST/$dst' && sudo podman push --tls-verify=false --digestfile /tmp/a2-push.digest '$REG_HOST/$dst' 2>&1 | tail -3; printf 'PUSH_DIGEST=%s\n' \"\$(sudo cat /tmp/a2-push.digest 2>/dev/null)\"" 2>&1
}
push_digest() { printf '%s\n' "$1" | sed -n 's/^PUSH_DIGEST=//p' | tail -1 | tr -d '\r'; }

# guest_build_bump <tag> <stamp> —— 在 guest 内从**已 load 的源镜像**派生一个只差
# 一个惰性标记文件的镜像。纯 LABEL 改动不改文件系统 → ostree commit 可能完全相同
# → digest 不变 → upgrade 无从判「旧→新」,所以标记文件是必要的(不改变任何行为)。
guest_build_bump() {
    local tag="$1" stamp="$2" b64
    b64="$(printf 'FROM %s\nLABEL daedalus.a2="bump=%s"\nRUN printf %%s %s > /etc/daedalus-a2-bump\n' \
        "$DRIVER_IMAGE_BASE" "$stamp" "bump=$stamp" | base64 -w0)"
    gssh "echo '$b64' | base64 -d | sudo tee /tmp/a2-bump.Containerfile >/dev/null && sudo podman build -t '$tag' -f /tmp/a2-bump.Containerfile /tmp 2>&1 | tail -2" 2>&1
}

# insecure registry:bootc 侧走 containers/image,读同一份 registries.conf。
# 漏这一条,bootc 得到的是 x509 错误,同样像「镜像不存在」。
guest_registry_insecure() {
    gssh "sudo mkdir -p /etc/containers/registries.conf.d && printf '[[registry]]\nlocation = \"127.0.0.1:$REG_PORT\"\ninsecure = true\n' | sudo tee /etc/containers/registries.conf.d/999-a2-probe.conf >/dev/null && cat /etc/containers/registries.conf.d/999-a2-probe.conf" 2>&1 | sed 's/^/      /' | tee -a "$DRIVER_LOG"
}

say "A2-1 · bootc install to-disk /dev/vdb(逐候选,成功即停)"
# 每个候选只试一次;--wipe 让每次尝试都从干净盘开始。
INS=""; INS_RC=1; SRC_USED=""
try_install() {
    local kind="$1" cmd="$2"
    INS="$(gssh "$cmd > /tmp/a2-install.log 2>&1; echo RC=\$?; tail -18 /tmp/a2-install.log" 2>&1)"
    INS_RC="$(printf '%s' "$INS" | sed -n 's/^RC=//p' | tail -1)"
    SRC_USED="$kind"
    info "候选=$kind → rc=${INS_RC:-?}"
    printf '%s\n' "$INS" | sed 's/^/        /' | tee -a "$DRIVER_LOG"
}
if [ -n "$CS_REF" ]; then
    # 候选 #1:bootc --help 要求的形态 —— 在**被安装的镜像**里跑 bootc。
    # --pid=host 是 bootc 自己会检查的前置条件(`This command must be run with the
    # podman --pid=host flag`);--privileged 让它看得见 /dev/vdb;
    # 把 guest 的 root storage 挂进去,它才能找到「自己这张镜像」。
    try_install "container($CS_REF)" "sudo timeout 1500 podman run --rm --privileged --pid=host --security-opt label=disable -v /var/lib/containers/storage:/var/lib/containers/storage --entrypoint /usr/bin/bootc '$CS_REF' install to-disk --wipe /dev/vdb"
    # 候选 #2/#3:从 booted 系统直接调 bootc(旧路径),取原始错误留证。
    if [ "${INS_RC:-1}" != "0" ]; then
        try_install "containers-storage:$CS_REF" "sudo timeout 1500 bootc install to-disk --wipe --source-imgref 'containers-storage:$CS_REF' /dev/vdb"
    fi
else
    driver_verdict FAIL "A2-1 源镜像没进 guest 的 root podman storage,后续候选都无从谈起"
fi
info "install rc=$INS_RC (source=${SRC_USED:-<无候选>})"

# 本地 registry 起在 install 之后:它要占 guest 的盘,而 install 期间盘要给镜像。
say "A2-2 前置 · guest 内本地 registry"
REG_OK=0
if [ -n "$CS_REF" ] && guest_registry_up; then
    REG_OK=1
    ok "本地 registry 已就绪($REG_HOST,http,镜像 ${REG_IMAGE:-?})"
    guest_registry_insecure
else
    driver_verdict FAIL "A2-2 前置不成立:guest 内 registry 起不来(podman / registry 镜像 / storage 之一,见上)"
fi

# 目标盘分区:根分区**不是**第一个 xfs(如果镜像带独立 /boot,那块也是 xfs),
# 挂错分区会让 ostree admin status 永远看不到部署。判据改成「该分区里有
# ostree/deploy」才算根分区。
PART="$(gssh "lsblk -ln -o NAME,FSTYPE /dev/vdb 2>&1" 2>&1)"
info "目标盘分区: $(printf '%s' "$PART" | tr '\n' ' ')"
ROOT_PART=""; OST=""; INST_ORIGIN=""
for p in $(printf '%s' "$PART" | awk '$2=="xfs"{print $1}'); do
    probe="$(gssh "sudo mkdir -p /mnt/a2probe && sudo mount -o ro /dev/$p /mnt/a2probe 2>/dev/null && (ls -d /mnt/a2probe/ostree/deploy 2>/dev/null || echo NOT_ROOT) && sudo umount /mnt/a2probe" 2>&1)"
    printf '%s\n' "$probe" | sed "s/^/      [候选根分区 $p] /" | tee -a "$DRIVER_LOG"
    if printf '%s' "$probe" | grep -q "ostree/deploy"; then ROOT_PART="$p"; break; fi
done
if [ -n "$ROOT_PART" ]; then
    OST="$(gssh "sudo mkdir -p /mnt/a2root && sudo mount -o ro /dev/$ROOT_PART /mnt/a2root && sudo ostree admin status --sysroot=/mnt/a2root 2>&1 | head -10; echo '---origin---'; sudo sh -c 'cat /mnt/a2root/ostree/deploy/*/deploy/*.origin 2>/dev/null | head -8'; sudo umount /mnt/a2root" 2>&1)"
    printf '%s\n' "$OST" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
    INST_ORIGIN="$(printf '%s\n' "$OST" | sed -n '/^---origin---$/,$p' | tail -n +2 | grep -E '^(ref|container-image-reference)' | head -1)"
    info "目标盘部署来源: ${INST_ORIGIN:-<读不到>}"
fi
if [ "${INS_RC:-1}" = "0" ] && printf '%s' "$OST" | grep -qE '[0-9a-f]{64}\.'; then
    driver_verdict PASS "A2-1 install to-disk 成功且目标 /dev/vdb 根分区($ROOT_PART)上有 ostree 部署(source=$SRC_USED)"
elif [ "${INS_RC:-1}" = "0" ]; then
    driver_verdict FAIL "A2-1 install 返回 0 但目标盘根分区上看不到 ostree 部署(分区探测: ${PART})"
elif [ -z "$CS_REF" ]; then
    driver_verdict FAIL "A2-1 无可用候选源(load 失败)"
else
    driver_verdict FAIL "A2-1 install to-disk 未成功(rc=${INS_RC:-?},source=$SRC_USED): $(printf '%s' "$INS" | grep -m1 -E '^error|Error' | tr -d '\n' | head -c 200)"
fi

# =============================================================================
# A2-2 · bootc upgrade:同一 ref、两个 digest
# =============================================================================
# 语义:升级 = 同一个 ref 的旧 digest → 新 digest。换成两个不同 repoTags 去
# 「升级」是 switch 语义,不是 upgrade 语义。
# 关键事实(2026-11-17 实测):`bootc switch` 到**同样的内容**不会改写部署的来源
# ref —— ostree commit 内容寻址,同内容 = 同 commit,commit 元数据里的
# container-image-reference 还是旧的。所以第一步必须切到**内容不同**的镜像,
# 让新 commit 带上 registry 的 ref。
say "A2-2 · bootc upgrade(同一 ref、两个 digest)"
gssh "sudo bootc switch --help 2>&1 | sed -n '1,14p'; echo '--- upgrade help ---'; sudo bootc upgrade --help 2>&1 | sed -n '1,14p'" 2>&1 | sed 's/^/      /' | tee -a "$DRIVER_LOG"
BEFORE_STATUS="$(bootc_json status)"
DIG_BEFORE="$(json_get "$BEFORE_STATUS" "status.booted.image.imageDigest")"
info "upgrade 前 booted 部署: ref=$(json_get "$BEFORE_STATUS" "status.booted.image.image") digest=${DIG_BEFORE:-<空>}"

DIG_A=""; DIG_B=""; DIG_C=""
if [ "$REG_OK" = 1 ]; then
    # A = 当前镜像原样推成 :latest(「旧 digest」那一端)。
    PUSH_A="$(guest_push "$CS_REF" "$REG_REPO:latest")"
    printf '%s\n' "$PUSH_A" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
    DIG_A="$(push_digest "$PUSH_A")"
    [ -n "$DIG_A" ] && driver_verdict PASS "A2-2a registry 可用且 A 已推上 $REG_HOST/$REG_REPO:latest(digest=$DIG_A)" \
        || driver_verdict FAIL "A2-2a A 推到本地 registry 失败"

    # B = 内容只差一个惰性标记文件的派生镜像(同一 tag 的另一个 digest)。
    guest_build_bump localhost/daedalus-verify:bump-a "a$(date +%s)" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
    PUSH_B="$(guest_push localhost/daedalus-verify:bump-a "$REG_REPO:latest")"
    printf '%s\n' "$PUSH_B" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
    DIG_B="$(push_digest "$PUSH_B")"
    if [ -n "$DIG_A" ] && [ -n "$DIG_B" ] && [ "$DIG_A" != "$DIG_B" ]; then
        driver_verdict PASS "A2-2b 同一 tag 上有两个 digest(A=$DIG_A B=$DIG_B)"
    else
        driver_verdict FAIL "A2-2b 同一 tag 上没拿到两个不同 digest(A=${DIG_A:-?} B=${DIG_B:-?})"
    fi

    # 把 booted 部署切到 registry 上的 B(内容不同 → 新 commit 带 registry ref)。
    SW="$(gssh "sudo timeout 1500 bootc switch --transport registry $REG_HOST/$REG_REPO:latest 2>&1; echo RC=\$?" 2>&1)"
    printf '%s\n' "$SW" | tail -10 | sed 's/^/      /' | tee -a "$DRIVER_LOG"
    SW_RC="$(printf '%s' "$SW" | sed -n 's/^RC=//p' | tail -1)"
    info "switch rc=$SW_RC"
    if [ "${SW_RC:-1}" = "0" ]; then
        OLD_BOOT="$(gssh 'cat /proc/sys/kernel/random/boot_id' 2>/dev/null | tr -d '\r')"
        gssh "sudo systemctl reboot" >/dev/null 2>&1 || true
        NEW_BOOT="$(driver_reboot_and_wait "$OLD_BOOT" "$DRIVER_BOOT_TIMEOUT" || true)"
        info "重启后 boot_id: ${NEW_BOOT:-<超时>}"
        # registry 容器随重启消失,数据在命名卷里 —— 起回来即可。
        if guest_registry_up; then
            ok "registry 重启后恢复(数据在命名卷 $REG_VOL)"
        else
            warn "registry 重启后没起来"
        fi
        AFTER_SW="$(bootc_json status)"
        REF_SW="$(json_get "$AFTER_SW" "status.booted.image.image")"
        DIG_SW="$(json_get "$AFTER_SW" "status.booted.image.imageDigest")"
        ORIGIN_SW="$(gssh "sudo sh -c 'cat /sysroot/ostree/deploy/*/deploy/*.origin 2>/dev/null | head -6'" 2>&1 | tr -d '\r')"
        printf '%s\n' "$ORIGIN_SW" | sed 's/^/      [origin] /' | tee -a "$DRIVER_LOG"
        info "switch 后 booted: ref=${REF_SW:-<空>} digest=${DIG_SW:-<空>}"
        if printf '%s' "$REF_SW$ORIGIN_SW" | grep -q "$REG_HOST/$REG_REPO:latest"; then
            # 注意:1.16.13 的 status.booted.image.image 有时仍显示旧 ref(commit 元数据),
            # 部署真正的来源看 origin 文件里的 container-image-reference —— 判据两者并看。
            driver_verdict PASS "A2-2c booted 部署的 origin 已指向 registry ref(status.image=${REF_SW:-<空>})"
        else
            driver_verdict FAIL "A2-2c booted 部署来源不是 registry ref(ref=${REF_SW:-<空>},期望含 $REG_HOST/$REG_REPO:latest)"
        fi
        DIG_BEFORE="$DIG_SW"

        # C = 同 tag 的第三个 digest:upgrade 的「新」那一端。
        guest_build_bump localhost/daedalus-verify:bump-b "c$(date +%s)" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
        PUSH_C="$(guest_push localhost/daedalus-verify:bump-b "$REG_REPO:latest")"
        printf '%s\n' "$PUSH_C" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
        DIG_C="$(push_digest "$PUSH_C")"
        info "C digest: ${DIG_C:-?}"
        CHK="$(gssh "sudo timeout 300 bootc upgrade --check 2>&1; echo RC=\$?" 2>&1)"
        printf '%s\n' "$CHK" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
        CHK_RC="$(printf '%s' "$CHK" | sed -n 's/^RC=//p' | tail -1)"
        if [ "${CHK_RC:-1}" = "0" ] && ! printf '%s' "$CHK" | grep -qiE 'no updates? available|up to date|already (up to date|current)'; then
            driver_verdict PASS "A2-2d bootc upgrade --check 报有更新(rc=$CHK_RC;booted=${DIG_BEFORE:-?} registry=${DIG_C:-?})"
        else
            driver_verdict FAIL "A2-2d bootc upgrade --check 没报有更新(rc=${CHK_RC:-?}): $(printf '%s' "$CHK" | tail -2 | tr '\n' ' ')"
        fi
        UP="$(gssh "sudo timeout 1500 bootc upgrade 2>&1; echo RC=\$?" 2>&1)"
        printf '%s\n' "$UP" | tail -12 | sed 's/^/      /' | tee -a "$DRIVER_LOG"
        UP_RC="$(printf '%s' "$UP" | sed -n 's/^RC=//p' | tail -1)"
        STAGED="$(bootc_json status)"
        DIG_STAGED="$(json_get "$STAGED" "status.staged.image.imageDigest")"
        info "upgrade 后 staged digest=${DIG_STAGED:-<空>}"
        if [ "${UP_RC:-1}" = "0" ] && [ -n "$DIG_STAGED" ]; then
            driver_verdict PASS "A2-2e bootc upgrade rc=0 且 staged 部署 digest=$DIG_STAGED"
        else
            driver_verdict FAIL "A2-2e bootc upgrade 未成功(rc=${UP_RC:-?},staged digest=${DIG_STAGED:-<空>})"
        fi
        # 重启让 staged 生效,再比 booted:同一 ref、digest 变了。
        OLD_BOOT2="$(gssh 'cat /proc/sys/kernel/random/boot_id' 2>/dev/null | tr -d '\r')"
        gssh "sudo systemctl reboot" >/dev/null 2>&1 || true
        driver_reboot_and_wait "$OLD_BOOT2" "$DRIVER_BOOT_TIMEOUT" >/dev/null || true
        FINAL="$(bootc_json status)"
        REF_FINAL="$(json_get "$FINAL" "status.booted.image.image")"
        DIG_FINAL="$(json_get "$FINAL" "status.booted.image.imageDigest")"
        BUMP_LIVE="$(gssh 'cat /etc/daedalus-a2-bump 2>/dev/null' 2>&1 | tr -d '\r')"
        info "重启后 booted: ref=${REF_FINAL:-<空>} digest=${DIG_FINAL:-<空>} 新内容=$(printf '%s' "$BUMP_LIVE")"
        REF_SAME=0
        [ -n "$REF_FINAL" ] && [ "${REF_FINAL%%@*}" = "${REG_HOST}/${REG_REPO}:latest" ] && REF_SAME=1
        if [ "$REF_SAME" = "1" ] && [ -n "$DIG_FINAL" ] && [ "$DIG_FINAL" != "$DIG_BEFORE" ]; then
            driver_verdict PASS "A2-2f 重启后 booted 部署同 ref(${REF_FINAL})且 digest 由 ${DIG_BEFORE:-?} 变为 ${DIG_FINAL}(新内容: $(printf '%s' "$BUMP_LIVE"))"
        else
            driver_verdict FAIL "A2-2f 重启后 booted 部署不满足「同 ref、digest 变」(ref=${REF_FINAL:-?},digest ${DIG_BEFORE:-?} → ${DIG_FINAL:-?})"
        fi
    else
        driver_verdict FAIL "A2-2c bootc switch 到本地 registry 失败(rc=${SW_RC:-?}): $(printf '%s' "$SW" | grep -m1 -E '^error' | head -c 160)"
    fi
fi

say "A2-3 · 宿主盘未被触碰"
HOST_LSBLK_AFTER="$("${HOST_LSBLK_CMD[@]}" 2>&1)"
printf '%s\n' "--- 宿主块设备(前) ---" "$HOST_LSBLK_BEFORE" "--- 宿主块设备(后) ---" "$HOST_LSBLK_AFTER" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
if [ "$HOST_LSBLK_BEFORE" = "$HOST_LSBLK_AFTER" ]; then
    driver_verdict PASS "A2-3 宿主块设备结构与文件系统 UUID 前后逐字节一致(install 目标是宿主上的一个 qcow2 文件)"
else
    driver_verdict FAIL "A2-3 宿主块设备结构/文件系统 UUID 发生变化(见上两段)"
fi

driver_exit
