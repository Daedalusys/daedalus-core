#!/usr/bin/env bash
#
# provision-kvm-hosts.sh — 在 KVM 宿主上幂等拉起四台异构被控机(vmu/vmd/vmc/vma),
# 并把**共享 PKI** 与 agent 配置预置进各 guest。
# 四台 = 三台异构(Ubuntu/Debian/CentOS)+ 一台 AlmaLinux 9(vma);
# vma 是 RHEL 9 行的**临时验证宿主**(内存按 RHEL 9 验收门槛 ≥2 GB 单列)。
#
# 三条不能破的不变量(改脚本前先读):
#   - 四台 guest 共用**同一张 CA**:agent 首启会在 ca_path/ca_key_path 自举一张自己的
#     CA,而控制面对一个 Plan 里的所有节点只持有一份客户端证书 —— 各持一套 CA 时
#     plan submit 谁都握不上手。故必须在 agent install 之前把同一张 CA 与各自签发
#     的 server 证书铺进 /etc/daedalus-agent/tls/(agent 只在 CertPath 缺失时才自举,
#     铺好即不自举)。
#   - server 证书 SAN 必须含 DNS:localhost:控制面在宿主上拨 127.0.0.1:<agentport>,
#     TLS 校验取 host 段当 ServerName。
#   - vmc 故意不装 rsync / btrfs:它要报 SNAPSHOT_CAPABILITY_NONE,充当"无回滚点"腿。
#     (只针对 vmc;vma 装 rsync,btrfs/snapper 留给后续单独的靶机。)
#
# 用法:
#   tests/fleet/provision-kvm-hosts.sh --up                    (默认)幂等拉起四台 guest
#   tests/fleet/provision-kvm-hosts.sh --status                打印各 guest 状态
#   tests/fleet/provision-kvm-hosts.sh --destroy [--purge-images]
#   tests/fleet/provision-kvm-hosts.sh --selftest              本机干跑(不 ssh/不碰 KVM/不联网)
#
# 环境变量:
#   DAEDALUS_KVM_HOST  KVM 宿主 ssh 别名(默认 Masterwet_Server)
#   DAEDALUS_KVM_DIR   宿主 $HOME 下的工作目录名(默认 daedalus-fleet-kvm)
#
# 退出码:0 全部成功;非 0 表示某一步失败(逐条打印,见 ERR 线索)。

set -euo pipefail

# 真机驱动最怕"静默中断":set -e 下任何意外失败都会让整轮以半截日志收场。
trap 'echo "provision-kvm-hosts: 意外失败(exit=$?),用 bash -x 重跑定位" >&2' ERR

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SELF="$ROOT/tests/fleet/provision-kvm-hosts.sh"

KVM_HOST="${DAEDALUS_KVM_HOST:-Masterwet_Server}"
FLEET_DIR_NAME="${DAEDALUS_KVM_DIR:-daedalus-fleet-kvm}"
SSH="${DAEDALUS_SSH:-ssh}"

pass=0
fail=0

# ================================================================= 共享库 ====
#
# 下面这段代码**本地与远端共用同一份**(远端程序由本地拼装上传),因此
# --selftest 断言的就是真跑时跑的那份逻辑,不存在两套实现漂移。

SHARED_LIB=$(cat <<'SHARED_EOF'
# --- 输出助手(远端程序也用,所以必须住共享库) ----------------------------
#
# pass/fail 由本地脚本初始化;远端程序不统计,只打印。
pass=${pass:-0}
fail=${fail:-0}
ok()   { echo "  ok   - $*"; pass=$((pass + 1)); }
bad()  { echo "  FAIL - $*" >&2; fail=$((fail + 1)); }
step() { echo; echo "== $*"; }

# --- 四台 guest 的事实表 ---------------------------------------------------
# 三台异构(Ubuntu / Debian / CentOS)+ 一台 AlmaLinux 9(vma,RHEL 9 行的临时验证宿主)。

GUEST_NAMES=(vmu vmd vmc vma)
GUEST_FAMILY=(ubuntu debian centos almalinux)
GUEST_AGENT_ID=(e2e-u24 e2e-d12 e2e-c9 e2e-a9)
GUEST_IMAGE=(
  noble-server-cloudimg-amd64.img
  debian-12-genericcloud-amd64.qcow2
  CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2
  # Alma 目录没有 -latest 别名(已实测),只能钉日期版本号;抬版本时改这里。
  AlmaLinux-9-GenericCloud-9.8-20261112.x86_64.qcow2
)
GUEST_IMAGE_URL=(
  https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
  https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2
  https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2
  https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/AlmaLinux-9-GenericCloud-9.8-20261112.x86_64.qcow2
)
GUEST_SSH_PORT=(9501 9502 9503 9504)
GUEST_AGENT_PORT=(9471 9472 9473 9474)

GUEST_USER="daedalus"          # guest 内建账号(cloud-init 创建)
# 宿主内存有限且已有负载:前三台各 1G 足够跑 apt/dnf 与一个静态 agent;
# vma 是 RHEL 9 行的临时验证宿主,验收门槛要求 ≥2 GB,故单列 2048。
GUEST_MEM_MB=(1024 1024 1024 2048)
GUEST_CPUS=2
GUEST_DISK_SIZE=12G
GUEST_AGENT_LISTEN=9443        # guest 内 agent 监听端口

FLEET_SSH_BEGIN="# BEGIN daedalus-fleet-kvm (managed by tests/fleet/provision-kvm-hosts.sh)"
FLEET_SSH_END="# END daedalus-fleet-kvm"

# fleet_layout_validate:端口/命名/镜像的静态自检。任一冲突即返回 1。
fleet_layout_validate() {
    local rc=0 i j n col cnt
    n=${#GUEST_NAMES[@]}
    [ "$n" -gt 0 ] || { echo "guest 事实表为空" >&2; rc=1; }
    # 各事实列长度必须与 GUEST_NAMES 一致:少一列会让下标错位(端口串台/镜像错配)。
    for col in GUEST_FAMILY GUEST_AGENT_ID GUEST_IMAGE GUEST_IMAGE_URL \
               GUEST_SSH_PORT GUEST_AGENT_PORT GUEST_MEM_MB; do
        eval "cnt=\${#$col[@]}"
        [ "$cnt" -eq "$n" ] || {
            echo "$col 有 $cnt 项,应与 GUEST_NAMES 的 $n 项一致" >&2; rc=1; }
    done
    for ((i = 0; i < n; i++)); do
        [ -n "${GUEST_NAMES[i]}" ]     || { echo "第 $i 台缺名字" >&2; rc=1; }
        [ -n "${GUEST_AGENT_ID[i]}" ]  || { echo "${GUEST_NAMES[i]} 缺 agent_id" >&2; rc=1; }
        [ -n "${GUEST_IMAGE[i]}" ]     || { echo "${GUEST_NAMES[i]} 缺基础镜像名" >&2; rc=1; }
        [ -n "${GUEST_IMAGE_URL[i]}" ] || { echo "${GUEST_NAMES[i]} 缺基础镜像 URL" >&2; rc=1; }
        # 内存必须逐台给(数组),且是正数
        case "${GUEST_MEM_MB[i]}" in
            ''|*[!0-9]*) echo "${GUEST_NAMES[i]} 内存非数字:${GUEST_MEM_MB[i]}" >&2; rc=1 ;;
            *) [ "${GUEST_MEM_MB[i]}" -gt 0 ] || {
                    echo "${GUEST_NAMES[i]} 内存非正数:${GUEST_MEM_MB[i]}" >&2; rc=1; } ;;
        esac
        for p in "${GUEST_SSH_PORT[i]}" "${GUEST_AGENT_PORT[i]}"; do
            case "$p" in
                ''|*[!0-9]*) echo "${GUEST_NAMES[i]} 端口非数字:$p" >&2; rc=1 ;;
                *) [ "$p" -ge 1024 ] && [ "$p" -le 65535 ] || {
                        echo "${GUEST_NAMES[i]} 端口越界:$p" >&2; rc=1; } ;;
            esac
        done
        # 同一台机器的 ssh 转发口与 agent 转发口不能相同
        [ "${GUEST_SSH_PORT[i]}" != "${GUEST_AGENT_PORT[i]}" ] || {
            echo "${GUEST_NAMES[i]} 的 ssh 与 agent 端口相同:${GUEST_SSH_PORT[i]}" >&2; rc=1; }
    done
    # 名字 / agent_id / 端口两两不重复
    for ((i = 0; i < n; i++)); do
        for ((j = i + 1; j < n; j++)); do
            [ "${GUEST_NAMES[i]}" != "${GUEST_NAMES[j]}" ] || { echo "名字重复:${GUEST_NAMES[i]}" >&2; rc=1; }
            [ "${GUEST_AGENT_ID[i]}" != "${GUEST_AGENT_ID[j]}" ] || { echo "agent_id 重复:${GUEST_AGENT_ID[i]}" >&2; rc=1; }
            [ "${GUEST_SSH_PORT[i]}" != "${GUEST_SSH_PORT[j]}" ] || { echo "ssh 端口重复:${GUEST_SSH_PORT[i]}" >&2; rc=1; }
            [ "${GUEST_AGENT_PORT[i]}" != "${GUEST_AGENT_PORT[j]}" ] || { echo "agent 端口重复:${GUEST_AGENT_PORT[i]}" >&2; rc=1; }
            [ "${GUEST_SSH_PORT[i]}" != "${GUEST_AGENT_PORT[j]}" ] || { echo "ssh 端口撞 agent 端口:${GUEST_SSH_PORT[i]}" >&2; rc=1; }
            [ "${GUEST_AGENT_PORT[i]}" != "${GUEST_SSH_PORT[j]}" ] || { echo "agent 端口撞 ssh 端口:${GUEST_AGENT_PORT[i]}" >&2; rc=1; }
        done
    done
    return $rc
}

# fleet_render_user_data <下标> <ssh 公钥>:cloud-init user-data。
fleet_render_user_data() {
    local i="$1" pubkey="$2" name groups pkgs
    name="${GUEST_NAMES[i]}"
    case "${GUEST_FAMILY[i]}" in
        almalinux|centos)
            groups="wheel"
            # CentOS / AlmaLinux 精简云镜像不一定带 sudo;而我们铺文件/装包全靠 sudo,
            # 所以让 cloud-init 在开机阶段把 sudo 装上(先有 sudo,后有文件)。
            pkgs="packages: [sudo]"
            ;;
        *)
            groups="sudo"
            pkgs=""
            ;;
    esac
    cat <<EOF
#cloud-config
hostname: $name
manage_etc_hosts: true
users:
  - name: $GUEST_USER
    gecos: Daedalus fleet guest
    shell: /bin/bash
    groups: [$groups]
    sudo: ALL=(ALL) NOPASSWD:ALL
    lock_passwd: true
    ssh_authorized_keys:
      - $pubkey
ssh_pwauth: false
disable_root: true
package_update: false
package_upgrade: false
$pkgs
growpart:
  mode: auto
  devices: ['/']
resize_rootfs: true
runcmd:
  - [ sh, -c, "echo provisioned-by=daedalus-fleet-kvm > /etc/daedalus-fleet-provisioned" ]
EOF
}

# fleet_render_meta_data <下标>:cloud-init meta-data。
fleet_render_meta_data() {
    local i="$1" name
    name="${GUEST_NAMES[i]}"
    cat <<EOF
instance-id: $name-001
local-hostname: $name
EOF
}

# fleet_render_agent_config <下标>:铺进 guest 的 /etc/daedalus-agent/config.yaml。
fleet_render_agent_config() {
    local i="$1" aid
    aid="${GUEST_AGENT_ID[i]}"
    cat <<EOF
# 由 tests/fleet/provision-kvm-hosts.sh 预置;不要手改。
# 四个 TLS 路径必须都非空,否则 agent 会把 mTLS 降级成"关闭"(见 agent main.go)。
agent_id: $aid
# 控制面跑在 KVM 宿主上;slirp 用户态网络里宿主的地址是 10.0.2.2。
control_plane_addr: https://10.0.2.2:7443
cert_path: /etc/daedalus-agent/tls/agent.pem
key_path: /etc/daedalus-agent/tls/agent.key
ca_path: /etc/daedalus-agent/tls/ca.pem
# 共享 CA 的私钥只留在宿主上,不铺进 guest:预置的 server 证书有效期 10 年,
# 不会触发 agent 的轮换路径(轮换需要 ca.key)。
ca_key_path: /etc/daedalus-agent/tls/ca.key
data_dir: /var/lib/daedalus-agent
audit_dir: /var/log/daedalus-agent
EOF
}

# fleet_render_ssh_config_block <宿主 HOME> <私钥绝对路径>:写进宿主 ~/.ssh/config
# 的标记块。放在文件**最前**:ssh 对每个参数取"最先命中的值",若用户自己的
# `Host *` 块在前面,我们的 User/IdentityFile 会被吃掉。
fleet_render_ssh_config_block() {
    local home="$1" key="$2" i n
    n=${#GUEST_NAMES[@]}
    printf '%s\n' "$FLEET_SSH_BEGIN"
    for ((i = 0; i < n; i++)); do
        cat <<EOF
Host ${GUEST_NAMES[i]}
    HostName 127.0.0.1
    Port ${GUEST_SSH_PORT[i]}
    User $GUEST_USER
    IdentityFile $key
    IdentitiesOnly yes
    StrictHostKeyChecking accept-new
    UserKnownHostsFile $home/$FLEET_DIR_NAME/known_hosts
    LogLevel ERROR
EOF
    done
    printf '%s\n' "$FLEET_SSH_END"
}

# fleet_upsert_ssh_config <目标文件> <标记块文件>:幂等地把标记块写进目标文件
# (已有则整块替换,内容原样保留)。块内容原样保留在文件**最前**。
fleet_upsert_ssh_config() {
    local target="$1" blockfile="$2" tmp body
    tmp="$(mktemp)"
    body="$(mktemp)"
    if [ -f "$target" ]; then
        awk -v b="$FLEET_SSH_BEGIN" -v e="$FLEET_SSH_END" '
            $0 == b { skip = 1; next }
            $0 == e { skip = 0; next }
            !skip { print }
        ' "$target" > "$body"
    else
        : > "$body"
    fi
    {
        cat "$blockfile"
        cat "$body"
    } > "$tmp"
    if [ -f "$target" ]; then
        chmod --reference="$target" "$tmp" 2>/dev/null || chmod 600 "$tmp"
    else
        chmod 600 "$tmp"
    fi
    mv "$tmp" "$target"
    rm -f "$body"
}

# fleet_remove_ssh_config_block <目标文件>:去掉标记块(销毁时用)。
fleet_remove_ssh_config_block() {
    local target="$1" tmp body
    [ -f "$target" ] || return 0
    tmp="$(mktemp)"
    body="$(mktemp)"
    awk -v b="$FLEET_SSH_BEGIN" -v e="$FLEET_SSH_END" '
        $0 == b { skip = 1; next }
        $0 == e { skip = 0; next }
        !skip { print }
    ' "$target" > "$body"
    cat "$body" > "$tmp"
    chmod --reference="$target" "$tmp" 2>/dev/null || chmod 600 "$tmp"
    mv "$tmp" "$target"
    rm -f "$body"
}

# fleet_remote_pkg_retry <guest> <标签> <远端命令>:包管理器会被云镜像自带的
# apt-daily / unattended-upgrades / dnf-makecache 抢锁,`cloud-init status --wait`
# 不等这些定时任务。这里等锁 + 重试,把"刚开机就撞锁"变成正常路径而不是失败。
# 放在共享层是为了 selftest 能直接验证"失败不会被吞成成功"。
fleet_remote_pkg_retry() {
    local name="$1" label="$2" cmd="$3" attempt=1 max=8 out=""
    while :; do
        if out="$(fleet_remote_ssh "$name" "$cmd" 2>&1)"; then
            return 0
        fi
        if [ "$attempt" -ge "$max" ]; then
            echo "  FAIL - $name:$label 失败(已重试 $attempt 次);原文:" >&2
            printf '%s\n' "$out" | sed 's/^/         /' >&2
            return 1
        fi
        echo "  ..   - $name:$label 撞锁/失败,第 $attempt 次重试前等 15s"
        sleep 15
        attempt=$((attempt + 1))
    done
}

fleet_remote_guest_prep_needed() {
    # 只在本台 guest "还没 prep 过"或"目标状态被破坏"时返回 0。
    #
    # 不能只看现状:Ubuntu 云镜像自带 rsync,只看现状会永远跳过 `apt-get update`,
    # 而云镜像的包索引是出厂快照,后面 agent-e2e 装包会撞"没有候选版本"。
    # 标记只在 prep 真正成功后写(见调用点),所以不会把失败吞成"已 prep";
    # 标记落在 vms/<guest>/ 里,guest 重建时目录一起重建,标记自然消失。
    local i="$1" name marker
    name="${GUEST_NAMES[i]}"
    marker="${FLEET_WORK:-$HOME/$FLEET_DIR_NAME}/vms/$name/.prepped"
    [ -f "$marker" ] || return 0
    case "${GUEST_FAMILY[i]}" in
        ubuntu|debian)
            fleet_remote_ssh "$name" 'command -v rsync >/dev/null 2>&1' && return 1
            return 0
            ;;
        almalinux)
            # vma 要 rsync(= rsync 回滚点);btrfs/snapper 故意留给后续单独的靶机。
            fleet_remote_ssh "$name" 'command -v rsync >/dev/null 2>&1' && return 1
            return 0
            ;;
        centos)
            fleet_remote_ssh "$name" \
                '! rpm -q rsync >/dev/null 2>&1 && ! rpm -q btrfs-progs >/dev/null 2>&1' && return 1
            return 0
            ;;
    esac
    return 0
}

# fleet_render_qemu_args <下标> <guest 目录>:qemu 命令行。
# 编码约定:**一行一个 argv 元素**(不是一行一个"选项+值")—— "-name" 与 "vmu"
# 必须分两行,否则 qemu 会把 "-name vmu" 当成一个未知选项而拒绝启动。
# 用户态网络 + hostfwd:宿主 127.0.0.1:<sshport>→guest:22、<agentport>→guest:9443。
fleet_render_qemu_args() {
    local i="$1" d="$2" name sp ap mem
    name="${GUEST_NAMES[i]}"; sp="${GUEST_SSH_PORT[i]}"; ap="${GUEST_AGENT_PORT[i]}"
    mem="${GUEST_MEM_MB[i]}"
    cat <<EOF
-name
$name
-machine
pc,accel=kvm
-cpu
host
-m
$mem
-smp
$GUEST_CPUS
-drive
file=$d/overlay.qcow2,if=virtio,format=qcow2
-drive
file=$d/seed.iso,media=cdrom,readonly=on
-netdev
user,id=net0,hostfwd=tcp:127.0.0.1:$sp-:22,hostfwd=tcp:127.0.0.1:$ap-:$GUEST_AGENT_LISTEN
-device
virtio-net-pci,netdev=net0
-display
none
-monitor
none
-serial
file:$d/console.log
-pidfile
$d/qemu.pid
-daemonize
EOF
}

# fleet_render_ssh_config_file <宿主 HOME> <私钥绝对路径>:脚本内部用的 ssh_config
# (与 ~/.ssh/config 里的标记块同源,但与用户自己的配置隔离)。
fleet_render_ssh_config_file() {
    local home="$1" key="$2" i n
    n=${#GUEST_NAMES[@]}
    for ((i = 0; i < n; i++)); do
        cat <<EOF
Host ${GUEST_NAMES[i]}
    HostName 127.0.0.1
    Port ${GUEST_SSH_PORT[i]}
    User $GUEST_USER
    IdentityFile $key
    IdentitiesOnly yes
    StrictHostKeyChecking accept-new
    UserKnownHostsFile $home/$FLEET_DIR_NAME/known_hosts
    LogLevel ERROR
EOF
    done
}
SHARED_EOF
)

eval "$SHARED_LIB"

# ================================================================= 远端库 ====
#
# 远端程序在 KVM 宿主上跑(所有资源都在那一侧),由本脚本拼装上传。

REMOTE_LIB=$(cat <<'REMOTE_EOF'
FLEET_WORK="${HOME}/${FLEET_DIR_NAME}"

fleet_remote_preflight() {
    local missing=0 t
    for t in qemu-system-x86_64 qemu-img cloud-localds openssl curl sha256sum awk ssh-keygen; do
        command -v "$t" >/dev/null 2>&1 || { echo "宿主缺 $t" >&2; missing=1; }
    done
    [ "$missing" -eq 0 ] || return 1
    if [ ! -r /dev/kvm ] || [ ! -w /dev/kvm ]; then
        echo "宿主 /dev/kvm 不可读写(需要 KVM 加速)" >&2
        return 1
    fi
    return 0
}

fleet_remote_layout() {
    mkdir -p "$FLEET_WORK" "$FLEET_WORK/images" "$FLEET_WORK/pki" \
             "$FLEET_WORK/keys" "$FLEET_WORK/vms"
}

fleet_remote_images() {
    local i name url img sz
    for i in "${!GUEST_NAMES[@]}"; do
        name="${GUEST_NAMES[i]}"; url="${GUEST_IMAGE_URL[i]}"
        img="$FLEET_WORK/images/${GUEST_IMAGE[i]}"
        if [ -s "$img" ] && qemu-img info "$img" >/dev/null 2>&1; then
            sz="$(du -h "$img" | cut -f1)"
            echo "  ok   - $name 基础镜像已有($img,$sz)"
            continue
        fi
        echo "  ..   - $name 基础镜像下载中:$url"
        if ! curl -fL --retry 3 --retry-delay 5 --connect-timeout 20 \
             -o "$img.part" "$url"; then
            echo "  FAIL - $name 基础镜像下载失败:$url" >&2
            rm -f "$img.part"
            return 1
        fi
        mv "$img.part" "$img"
        sz="$(du -h "$img" | cut -f1)"
        echo "  ok   - $name 基础镜像就绪($img,$sz)"
    done
}

fleet_remote_keys() {
    local k="$FLEET_WORK/keys/id_ed25519"
    if [ ! -s "$k" ]; then
        ssh-keygen -q -t ed25519 -N "" -C "daedalus-fleet-kvm" -f "$k"
    fi
    chmod 600 "$k"
    chmod 644 "$k.pub"
}

fleet_remote_pki() {
    local p="$FLEET_WORK/pki" i name ext
    mkdir -p "$p"
    if [ ! -s "$p/ca.pem" ] || [ ! -s "$p/ca.key" ]; then
        openssl req -x509 -newkey rsa:4096 -nodes -keyout "$p/ca.key" -out "$p/ca.pem" \
            -days 3650 -subj "/O=Daedalusys/CN=daedalus-fleet-e2e-ca" \
            -addext "basicConstraints=critical,CA:TRUE" \
            -addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null 2>&1
        chmod 600 "$p/ca.key"
        echo "  ok   - 共享 CA 已生成:$p/ca.pem"
    else
        echo "  ok   - 共享 CA 复用:$p/ca.pem"
    fi
    for i in "${!GUEST_NAMES[@]}"; do
        name="${GUEST_NAMES[i]}"
        if [ -s "$p/$name.pem" ] && [ -s "$p/$name.key" ]; then
            echo "  ok   - $name server 证书复用:$p/$name.pem"
            continue
        fi
        openssl req -new -newkey rsa:2048 -nodes -keyout "$p/$name.key" \
            -out "$p/$name.csr" -subj "/O=Daedalusys/CN=$name" >/dev/null 2>&1
        ext="$p/$name.ext"
        cat > "$ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:localhost,IP:127.0.0.1
EOF
        openssl x509 -req -in "$p/$name.csr" -CA "$p/ca.pem" -CAkey "$p/ca.key" \
            -CAcreateserial -out "$p/$name.pem" -days 3650 -sha256 -extfile "$ext" \
            >/dev/null 2>&1
        rm -f "$p/$name.csr" "$ext"
        chmod 600 "$p/$name.key"
        echo "  ok   - $name server 证书已签发(SAN=DNS:localhost):$p/$name.pem"
    done
    if [ ! -s "$p/client.pem" ] || [ ! -s "$p/client.key" ]; then
        openssl req -new -newkey rsa:2048 -nodes -keyout "$p/client.key" \
            -out "$p/client.csr" -subj "/O=Daedalusys/CN=daedalus-fleet-client" >/dev/null 2>&1
        cat > "$p/client.ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=clientAuth
EOF
        openssl x509 -req -in "$p/client.csr" -CA "$p/ca.pem" -CAkey "$p/ca.key" \
            -CAcreateserial -out "$p/client.pem" -days 3650 -sha256 -extfile "$p/client.ext" \
            >/dev/null 2>&1
        rm -f "$p/client.csr" "$p/client.ext"
        chmod 600 "$p/client.key"
        echo "  ok   - 控制面客户端证书已签发:$p/client.pem"
    else
        echo "  ok   - 控制面客户端证书复用:$p/client.pem"
    fi
}

fleet_remote_ssh_config() {
    local home="$HOME" key="$FLEET_WORK/keys/id_ed25519" block work_cfg
    block="$(mktemp)"
    work_cfg="$(mktemp)"
    fleet_render_ssh_config_block "$home" "$key" > "$block"
    fleet_render_ssh_config_file "$home" "$key" > "$work_cfg"
    install -m 600 "$work_cfg" "$FLEET_WORK/ssh_config"
    fleet_upsert_ssh_config "$home/.ssh/config" "$block"
    rm -f "$block" "$work_cfg"
    echo "  ok   - 宿主 ~/.ssh/config 已写入标记块(可 ssh vmu/vmd/vmc/vma)"
}

fleet_remote_ssh() {
    local name="$1"; shift
    ssh -F "$FLEET_WORK/ssh_config" -o BatchMode=yes -o ConnectTimeout=10 "$name" "$@"
}

fleet_remote_pid_alive() {
    local pidfile="$1" pid
    [ -s "$pidfile" ] || return 1
    pid="$(cat "$pidfile" 2>/dev/null || true)"
    case "$pid" in
        ''|*[!0-9]*) return 1 ;;
    esac
    kill -0 "$pid" 2>/dev/null || return 1
    # 防误杀:pid 复用后可能不是 qemu 了
    grep -qa "qemu-system" "/proc/$pid/cmdline" 2>/dev/null || return 1
    return 0
}

fleet_remote_wait_ssh() {
    local name="$1" port="$2" limit="${3:-300}" waited=0 out
    while [ "$waited" -lt "$limit" ]; do
        if out="$(fleet_remote_ssh "$name" true 2>&1)"; then
            return 0
        fi
        # 重建 guest 会换 ssh 主机密钥,而 known_hosts 里留着上一台的旧键。
        # accept-new 只接受"没见过"的主机,对"换过钥匙"的主机仍然硬拒 ——
        # 这里把旧键摘掉重试一次,免得销毁重建后被自己的历史卡住。
        if printf '%s' "$out" | grep -q "REMOTE HOST IDENTIFICATION HAS CHANGED"; then
            ssh-keygen -q -R "[127.0.0.1]:$port" -f "$FLEET_WORK/known_hosts" >/dev/null 2>&1 || true
            echo "  ..   - $name:known_hosts 里的旧主机密钥已摘除(guest 被重建过)"
        fi
        sleep 5
        waited=$((waited + 5))
    done
    echo "  FAIL - $name 在 ${limit}s 内 ssh 不通" >&2
    printf '%s\n' "$out" | sed 's/^/       /' >&2
    return 1
}

fleet_remote_put() {
    # <guest> <本地源文件> <远端目标> <权限>:幂等铺文件(内容相同就跳过)。
    local name="$1" src="$2" dst="$3" mode="$4" lsum rsum
    lsum="$(sha256sum "$src" | cut -d' ' -f1)"
    rsum="$(fleet_remote_ssh "$name" "sudo sha256sum '$dst' 2>/dev/null" 2>/dev/null | cut -d' ' -f1 || true)"
    if [ -n "$rsum" ] && [ "$rsum" = "$lsum" ]; then
        echo "  ok   - $name:$dst 已是目标内容(sha256 ${lsum:0:12}),跳过"
        return 0
    fi
    if ! fleet_remote_ssh "$name" \
        "sudo mkdir -p \$(dirname '$dst') && sudo tee '$dst' >/dev/null && sudo chmod $mode '$dst'" \
        < "$src"; then
        # 显式判失败:本函数在条件列表里被调用时 set -e 是关的,漏判就会
        # 把"没铺上"报成"已铺设"。
        echo "  FAIL - $name:$dst 铺设失败" >&2
        return 1
    fi
    echo "  ok   - $name:$dst 已铺设(sha256 ${lsum:0:12},mode $mode)"
}

fleet_remote_guest_config() {
    # 把共享 CA + 各自 server 证书 + config.yaml 铺进 guest(必须在 agent install 之前)。
    local i="$1" name d p
    name="${GUEST_NAMES[i]}"; d="$FLEET_WORK/vms/$name"; p="$FLEET_WORK/pki"
    fleet_render_agent_config "$i" > "$d/config.yaml"
    fleet_remote_put "$name" "$p/$name.pem" /etc/daedalus-agent/tls/agent.pem 644 || return 1
    fleet_remote_put "$name" "$p/$name.key" /etc/daedalus-agent/tls/agent.key 600 || return 1
    fleet_remote_put "$name" "$p/ca.pem"    /etc/daedalus-agent/tls/ca.pem 644 || return 1
    fleet_remote_put "$name" "$d/config.yaml" /etc/daedalus-agent/config.yaml 600 || return 1
}

# fleet_remote_pkg_retry 在共享层(见上)。

fleet_remote_guest_packages() {
    # 刷包索引;Ubuntu/Debian 装 rsync(= rsync 回滚点),CentOS 保持没有 rsync/btrfs;
    # AlmaLinux(vma)也走 dnf,但装 rsync(有回滚点),不套 CentOS 那套"卸 rsync/btrfs"。
    local i="$1" name
    name="${GUEST_NAMES[i]}"
    case "${GUEST_FAMILY[i]}" in
        ubuntu|debian)
            fleet_remote_pkg_retry "$name" "apt-get update + rsync" \
                'sudo DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=120 update -qq && sudo DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=120 install -y -qq rsync' \
                || return 1
            echo "  ok   - $name:apt-get update + rsync 已装"
            ;;
        almalinux)
            fleet_remote_pkg_retry "$name" "dnf makecache" 'sudo LC_ALL=C dnf -y makecache' || return 1
            # dnf 装 rsync(= rsync 回滚点)。btrfs/snapper 故意不碰:
            # 它们留给后续单独的靶机(agent#8),vma 只当 RHEL 9 行的临时验证宿主。
            fleet_remote_pkg_retry "$name" "dnf install rsync" \
                'sudo LC_ALL=C dnf -y install rsync' || return 1
            # 断言结果而不是假定命令成功:rsync 必须在位(有回滚点)。
            local rsync_path
            rsync_path="$(fleet_remote_ssh "$name" 'command -v rsync 2>/dev/null || true' 2>/dev/null || true)"
            if [ -z "$rsync_path" ]; then
                echo "  FAIL - $name:rsync 没装上(会退化成无回滚点)" >&2
                return 1
            fi
            echo "  ok   - $name:dnf makecache + rsync 已装(有回滚点)"
            ;;
        centos)
            fleet_remote_pkg_retry "$name" "dnf makecache" 'sudo LC_ALL=C dnf -y makecache' || return 1
            # RPM 的 stdout 很吵(「No packages marked for removal.」之类),这里整段静音;
            # 失败与否由上面的 makecache 与最后的 rpm -q 复核决定。
            fleet_remote_ssh "$name" \
                'for p in rsync btrfs-progs; do if rpm -q "$p" >/dev/null 2>&1; then sudo LC_ALL=C dnf -y remove "$p" >/dev/null 2>&1; fi; done; true' \
                >/dev/null 2>&1 || true
            # 断言结果而不是假定命令成功:CentOS 要的就是"没有 rsync / btrfs"
            # (agent 才会探到 SNAPSHOT_CAPABILITY_NONE)。
            local still
            still="$(fleet_remote_ssh "$name" \
                'for p in rsync btrfs-progs; do rpm -q "$p" >/dev/null 2>&1 && echo "$p"; done; true' 2>/dev/null || true)"
            if [ -n "$still" ]; then
                echo "  FAIL - $name:仍装着 $still(无法得到 SNAPSHOT_CAPABILITY_NONE)" >&2
                return 1
            fi
            echo "  ok   - $name:dnf makecache + 已确保无 rsync / btrfs(SNAPSHOT_CAPABILITY_NONE)"
            ;;
    esac
}

fleet_remote_guest_up() {
    local i="$1" name d img fmt pid
    name="${GUEST_NAMES[i]}"; d="$FLEET_WORK/vms/$name"
    img="$FLEET_WORK/images/${GUEST_IMAGE[i]}"
    mkdir -p "$d"

    if fleet_remote_pid_alive "$d/qemu.pid"; then
        pid="$(cat "$d/qemu.pid")"
        echo "  ok   - $name:已在跑(pid $pid),复用不重建"
    else
        rm -f "$d/qemu.pid"
        fmt="$(qemu-img info "$img" 2>/dev/null | sed -n 's/^image format: //p' | head -1)"
        [ -n "$fmt" ] || fmt=qcow2
        if [ ! -s "$d/overlay.qcow2" ]; then
            if ! qemu-img create -f qcow2 -F "$fmt" -b "$img" "$d/overlay.qcow2" "$GUEST_DISK_SIZE" >/dev/null; then
                echo "  FAIL - $name:overlay 创建失败" >&2
                return 1
            fi
            echo "  ok   - $name:overlay 已建($GUEST_DISK_SIZE,backing $fmt)"
        else
            echo "  ok   - $name:overlay 复用:$d/overlay.qcow2"
        fi
        fleet_render_user_data "$i" "$(cat "$FLEET_WORK/keys/id_ed25519.pub")" > "$d/user-data"
        fleet_render_meta_data "$i" > "$d/meta-data"
        if ! cloud-localds "$d/seed.iso" "$d/user-data" "$d/meta-data" >/dev/null; then
            echo "  FAIL - $name:NoCloud seed 生成失败" >&2
            return 1
        fi
        echo "  ok   - $name:NoCloud seed 已造:$d/seed.iso"
        local -a qargs=()
        while IFS= read -r line; do qargs+=("$line"); done < <(fleet_render_qemu_args "$i" "$d")
        # 渲染约定是一行一个 argv 元素;一旦有人把"选项+值"写在一行,
        # qemu 会报 "invalid option",所以起之前先在本地把这类错误挡住。
        local bad_arg
        bad_arg="$(printf '%s\n' "${qargs[@]}" | grep -n '[[:space:]]' | head -1 || true)"
        if [ -n "$bad_arg" ]; then
            echo "  FAIL - $name:qemu 参数渲染有空格(第 $bad_arg 行):一行只能有一个 argv 元素" >&2
            return 1
        fi
        if ! qemu-system-x86_64 "${qargs[@]}"; then
            echo "  FAIL - $name:qemu 启动失败(见 $d/console.log)" >&2
            return 1
        fi
        pid="$(cat "$d/qemu.pid" 2>/dev/null || echo '?')"
        echo "  ok   - $name:qemu 已启动(pid $pid)"
    fi

    if ! fleet_remote_wait_ssh "$name" "${GUEST_SSH_PORT[i]}" 420; then
        echo "  FAIL - $name:ssh 不通" >&2
        return 1
    fi
    echo "  ok   - $name:ssh 已通"

    # cloud-init 跑完再动包管理器(否则撞 apt/dnf 锁)。
    fleet_remote_ssh "$name" 'sudo cloud-init status --wait >/dev/null 2>&1 || true' >/dev/null 2>&1 || true

    # 配置每次都对账(sha256 相同即跳过,便宜且能收敛);prep(刷包索引 + 装/卸
    # rsync/btrfs)只在"本台还没 prep 过"或"目标状态被破坏"时做(慢)。
    #
    # 注意:本函数是在 `fleet_remote_guest_up "$i" || return 1` 这种条件列表里被
    # 调用的,POSIX 会把 set -e 在整个函数体内关掉 —— 所以这里每个可能失败的命令
    # 都必须显式 `|| return 1`,否则失败会被吞掉、`.prepped` 还会被误写。
    fleet_remote_guest_config "$i" || return 1
    if fleet_remote_guest_prep_needed "$i"; then
        fleet_remote_guest_packages "$i" || return 1
        # 标记只在 prep 真的成功后写 —— 失败时下一轮还会重试,不会被"已 prep"掩盖。
        : > "$d/.prepped" || return 1
        echo "  ok   - $name:prep 完成(标记 $d/.prepped)"
    else
        echo "  ok   - $name:prep 已做过且状态正确,跳过装包"
    fi
}

# fleet_remote_verify:收敛校验 —— 全部读 guest 里的事实,不看脚本自报。
# agent_id 必须逐台不同、rsync 有无必须符合"给回滚点/不给回滚点"的分工、
# TLS 材料必须齐(agent 首启才不会自举 CA 把预置材料顶掉)。
fleet_remote_verify() {
    local i name not_ok=0 aid rs tls
    step "收敛校验(读 guest 内的事实)"
    for i in "${!GUEST_NAMES[@]}"; do
        name="${GUEST_NAMES[i]}"
        aid="$(fleet_remote_ssh "$name" \
            'sudo sed -n "s/^agent_id:[[:space:]]*//p" /etc/daedalus-agent/config.yaml' 2>/dev/null || true)"
        if [ "$aid" = "${GUEST_AGENT_ID[i]}" ]; then
            echo "  ok   - $name:agent_id=$aid"
        else
            echo "  FAIL - $name:agent_id 期望 ${GUEST_AGENT_ID[i]},实际 '$aid'" >&2
            not_ok=1
        fi
        tls="$(fleet_remote_ssh "$name" \
            'sudo test -s /etc/daedalus-agent/tls/agent.pem && sudo test -s /etc/daedalus-agent/tls/agent.key && sudo test -s /etc/daedalus-agent/tls/ca.pem && echo yes || echo no' \
            2>/dev/null || true)"
        if [ "$tls" = "yes" ]; then
            echo "  ok   - $name:TLS 材料齐(agent.pem/agent.key/ca.pem)"
        else
            echo "  FAIL - $name:TLS 材料不全" >&2
            not_ok=1
        fi
        rs="$(fleet_remote_ssh "$name" 'command -v rsync 2>/dev/null || true' 2>/dev/null || true)"
        case "${GUEST_FAMILY[i]}" in
            ubuntu|debian|almalinux)
                if [ -n "$rs" ]; then
                    echo "  ok   - $name:rsync 在位($rs)(宿主手段之一;回滚点仍按 kind 判,package/service 不因此有回滚点)"
                else
                    echo "  FAIL - $name:缺 rsync(会退化成无回滚点)" >&2
                    not_ok=1
                fi
                ;;
            centos)
                if [ -z "$rs" ]; then
                    echo "  ok   - $name:无 rsync → SNAPSHOT_CAPABILITY_NONE"
                else
                    echo "  FAIL - $name:不该有 rsync($rs)" >&2
                    not_ok=1
                fi
                ;;
        esac
    done
    [ "$not_ok" -eq 0 ]
}

fleet_remote_up() {
    local i
    step "宿主预检"
    if ! fleet_remote_preflight; then
        return 1
    fi
    echo "  ok   - qemu-system-x86_64 / qemu-img / cloud-localds / openssl 齐备,/dev/kvm 可写"

    fleet_remote_layout
    fleet_layout_validate || return 1

    step "基础镜像(不覆盖已下载的)"
    fleet_remote_images || return 1

    step "guest ssh 密钥"
    fleet_remote_keys
    echo "  ok   - ssh 密钥:$FLEET_WORK/keys/id_ed25519"

    step "共享 PKI(一张 CA + 每台一份 server 证书 + 一张控制面客户端证书)"
    fleet_remote_pki

    step "宿主 ssh 配置"
    fleet_remote_ssh_config

    step "四台 guest"
    for i in "${!GUEST_NAMES[@]}"; do
        fleet_remote_guest_up "$i" || return 1
    done

    fleet_remote_verify || return 1

    step "宿主上的产物"
    echo "  CA:            $FLEET_WORK/pki/ca.pem"
    echo "  控制面客户端:   $FLEET_WORK/pki/client.pem / $FLEET_WORK/pki/client.key"
    for i in "${!GUEST_NAMES[@]}"; do
        echo "  ${GUEST_NAMES[i]} server: $FLEET_WORK/pki/${GUEST_NAMES[i]}.pem"
    done
    echo "  guest 内已预置: /etc/daedalus-agent/config.yaml + tls/{agent.pem,agent.key,ca.pem}"
}

fleet_remote_status() {
    # 局部变量别叫 bad —— 共享库里 bad() 是个函数,同名局部变量会把它遮掉。
    local i name d pid alive sp ap ssh_state fwd_state listener not_ok=0
    step "宿主:$HOSTNAME($(id -un))"
    step "四台被控机"
    for i in "${!GUEST_NAMES[@]}"; do
        name="${GUEST_NAMES[i]}"; d="$FLEET_WORK/vms/$name"
        sp="${GUEST_SSH_PORT[i]}"; ap="${GUEST_AGENT_PORT[i]}"
        pid="$(cat "$d/qemu.pid" 2>/dev/null || echo '-')"
        alive="no"
        if fleet_remote_pid_alive "$d/qemu.pid"; then alive="yes"; fi
        [ "$alive" = "yes" ] || not_ok=1
        ssh_state="down"
        if fleet_remote_ssh "$name" true >/dev/null 2>&1; then ssh_state="ok"; fi
        [ "$ssh_state" = "ok" ] || not_ok=1
        # 只探测 qemu 的 hostfwd 监听:slirp 会先接受 TCP 连接,所以 open ≠ guest 内
        # 一定有进程在听;guest 侧监听状态另由下面 ss 一段确认。
        fwd_state="closed"
        if (exec 3<>"/dev/tcp/127.0.0.1/$ap") 2>/dev/null; then fwd_state="open"; fi
        listener="n/a"
        if [ "$ssh_state" = "ok" ]; then
            if fleet_remote_ssh "$name" \
                "sudo ss -ltn 2>/dev/null | grep -q ':$GUEST_AGENT_LISTEN ' && echo yes || echo no" \
                2>/dev/null | grep -q yes; then
                listener="yes"
            else
                listener="no"
            fi
        fi
        printf '  %-4s pid=%-8s alive=%-3s ssh=127.0.0.1:%s(%s) agent=127.0.0.1:%s(fwd=%s) guest:%s监听=%s\n' \
            "$name" "$pid" "$alive" "$sp" "$ssh_state" "$ap" "$fwd_state" "$GUEST_AGENT_LISTEN" "$listener"
        if [ "$ssh_state" = "ok" ]; then
            echo "       guest 内: $(fleet_remote_ssh "$name" 'sudo sed -n "s/^agent_id:[[:space:]]*//p" /etc/daedalus-agent/config.yaml' 2>/dev/null || true)"
            fleet_remote_ssh "$name" \
                'sudo ls -l /etc/daedalus-agent/config.yaml /etc/daedalus-agent/tls/agent.pem /etc/daedalus-agent/tls/agent.key /etc/daedalus-agent/tls/ca.pem 2>&1' \
                2>/dev/null | sed 's/^/       /' || true
        fi
    done
    step "宿主上的产物"
    ls -l "$FLEET_WORK/pki" 2>/dev/null | sed 's/^/  /' || true
    if [ "$not_ok" -eq 0 ]; then
        echo "  ok   - 四台 guest 全部在跑且 ssh 可达"
    else
        echo "  FAIL - 至少一台 guest 没起来或 ssh 不通(见上)" >&2
    fi
}

fleet_remote_destroy() {
    local purge="${1:-0}" i name d pid
    step "停机与清理"
    for i in "${!GUEST_NAMES[@]}"; do
        name="${GUEST_NAMES[i]}"; d="$FLEET_WORK/vms/$name"
        if fleet_remote_pid_alive "$d/qemu.pid"; then
            pid="$(cat "$d/qemu.pid")"
            kill "$pid" 2>/dev/null || true
            local waited=0
            while [ "$waited" -lt 30 ] && kill -0 "$pid" 2>/dev/null; do
                sleep 1; waited=$((waited + 1))
            done
            kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null || true
            echo "  ok   - $name:qemu(pid $pid)已停"
        else
            echo "  ok   - $name:未在跑"
        fi
    done
    for i in "${!GUEST_NAMES[@]}"; do
        name="${GUEST_NAMES[i]}"; d="$FLEET_WORK/vms/$name"
        rm -rf "$d"
        echo "  ok   - $name:overlay/seed/pid/log 已删($d)"
    done
    rm -f "$FLEET_WORK/ssh_config"
    fleet_remove_ssh_config_block "$HOME/.ssh/config"
    echo "  ok   - 宿主 ~/.ssh/config 标记块已移除"
    if [ "$purge" = "1" ]; then
        rm -rf "$FLEET_WORK/images"
        echo "  ok   - 基础镜像已删(--purge-images)"
    else
        echo "  ok   - 基础镜像保留在 $FLEET_WORK/images(要删加 --purge-images)"
    fi
    echo "  ok   - 共享 PKI 与 ssh 密钥保留在 $FLEET_WORK/pki、$FLEET_WORK/keys"
}

fleet_remote_main() {
    local mode="$1"; shift || true
    case "$mode" in
        up) fleet_remote_up "$@" ;;
        status) fleet_remote_status "$@" ;;
        destroy) fleet_remote_destroy "$@" ;;
        *) echo "远端程序:未知模式 $mode" >&2; exit 2 ;;
    esac
}

fleet_remote_main "$@"
REMOTE_EOF
)

# ================================================================== 本地层 ====

build_remote_program() {
    {
        printf '%s\n' '#!/usr/bin/env bash'
        printf '%s\n' '# 由 tests/fleet/provision-kvm-hosts.sh 生成并上传到 KVM 宿主;请勿手改。'
        printf '%s\n' 'set -euo pipefail'
        printf '%s\n' "FLEET_DIR_NAME='$FLEET_DIR_NAME'"
        printf '%s\n' "$SHARED_LIB"
        printf '%s\n' "$REMOTE_LIB"
    }
}

remote_program_path() {
    printf '~/%s/remote.sh' "$FLEET_DIR_NAME"
}

ensure_remote_program() {
    local local_sum remote_sum
    local_sum="$(build_remote_program | sha256sum | cut -d' ' -f1)"
    remote_sum="$("$SSH" -o BatchMode=yes -o ConnectTimeout=15 "$KVM_HOST" \
        "sha256sum $(remote_program_path) 2>/dev/null | cut -d' ' -f1" 2>/dev/null || true)"
    if [ -n "$remote_sum" ] && [ "$remote_sum" = "$local_sum" ]; then
        return 0
    fi
    build_remote_program | "$SSH" -o BatchMode=yes -o ConnectTimeout=15 "$KVM_HOST" \
        "mkdir -p ~/$FLEET_DIR_NAME && cat > $(remote_program_path) && chmod +x $(remote_program_path)"
}

run_remote() {
    local mode="$1"; shift
    ensure_remote_program
    "$SSH" -o BatchMode=yes -o ConnectTimeout=20 "$KVM_HOST" \
        "bash $(remote_program_path) $mode $*"
}

# 把远端程序摊到临时目录再执行,用于在本地干跑远端逻辑(自检用)。
dump_remote_program() {
    build_remote_program
}

# ====================================================== selftest(干跑断言) ===

fleet_parse_args() {
    MODE=""
    PURGE=0
    while [ $# -gt 0 ]; do
        case "$1" in
            --up)         MODE="up" ;;
            --status)     MODE="status" ;;
            --destroy)    MODE="destroy" ;;
            --selftest)   MODE="selftest" ;;
            --purge-images) PURGE=1 ;;
            --host)       shift; KVM_HOST="${1:-}"
                          [ -n "$KVM_HOST" ] || { echo "--host 需要一个 ssh 别名" >&2; return 2; } ;;
            -h|--help)    usage; exit 0 ;;
            *)            echo "未知参数:$1" >&2; return 2 ;;
        esac
        shift
    done
    [ -n "$MODE" ] || MODE="up"
    if [ "$PURGE" -eq 1 ] && [ "$MODE" != "destroy" ]; then
        echo "--purge-images 只在 --destroy 时有意义" >&2
        return 2
    fi
    return 0
}

usage() {
    sed -n '2,30p' "${BASH_SOURCE[0]}"
}

run_selftest() {
    local tmp rc out block
    tmp="$(mktemp -d)"

    step "参数解析(真调用本脚本,断言退出码)"
    if "$BASH" "$SELF" --help >/dev/null 2>&1; then
        ok "--help 退出 0"
    else
        bad "--help 退出码不是 0"
    fi
    rc=0
    "$BASH" "$SELF" --definitely-not-a-flag >/dev/null 2>&1 || rc=$?
    if [ "$rc" -eq 2 ]; then
        ok "未知参数退出 2(实际 $rc)"
    else
        bad "未知参数退出码应为 2,实际 $rc"
    fi
    rc=0
    "$BASH" "$SELF" --purge-images >/dev/null 2>&1 || rc=$?
    if [ "$rc" -eq 2 ]; then
        ok "--purge-images 不带 --destroy 被拒(退出 2)"
    else
        bad "--purge-images 单独使用应退出 2,实际 $rc"
    fi
    rc=0
    ( fleet_parse_args --status ) || rc=$?
    if [ "$rc" -eq 0 ]; then
        ok "参数解析:合法参数退出 0"
    else
        bad "参数解析:合法参数退出码 $rc"
    fi
    if ( fleet_parse_args --status; [ "$MODE" = "status" ] ); then
        ok "参数解析:MODE 解析为 status"
    else
        bad "参数解析:MODE 不是 status"
    fi
    if ( fleet_parse_args; [ "$MODE" = "up" ] ); then
        ok "参数解析:缺省模式是 up"
    else
        bad "参数解析:缺省模式不是 up"
    fi

    step "端口与布局分配(自检 + 反证)"
    if fleet_layout_validate; then
        ok "${#GUEST_NAMES[@]} 台 guest 的名字/agent_id/端口无冲突"
    else
        bad "布局自检没过"
    fi
    if ( GUEST_SSH_PORT[1]=9501; fleet_layout_validate ) >/dev/null 2>&1; then
        bad "把两台的 ssh 端口改成同一个居然没被拒 —— 端口自检形同虚设"
    else
        ok "反证:两台 ssh 端口撞车被拒"
    fi
    if ( GUEST_AGENT_PORT[2]=9503; fleet_layout_validate ) >/dev/null 2>&1; then
        bad "agent 端口撞 ssh 端口居然没被拒"
    else
        ok "反证:agent 端口撞 ssh 端口被拒"
    fi
    if fleet_layout_validate >/dev/null 2>&1; then
        ok "端口均在 1024-65535 且两两不同"
    else
        bad "端口自检没过"
    fi
    # 内存列长度与名字列不一致时必须被拒(否则下标错位,静默用错内存)。
    if ( GUEST_MEM_MB=("${GUEST_MEM_MB[@]}" 2048); fleet_layout_validate ) >/dev/null 2>&1; then
        bad "内存列多出一项居然没被拒 —— 各列长度校验形同虚设"
    else
        ok "反证:GUEST_MEM_MB 与 GUEST_NAMES 长度不一致被拒"
    fi

    step "guest 事实表(三台异构 + 一台 AlmaLinux)"
    if [ "${#GUEST_NAMES[@]}" -eq 4 ] &&
        [ "${GUEST_NAMES[3]}" = "vma" ] &&
        [ "${GUEST_FAMILY[3]}" = "almalinux" ] &&
        [ "${GUEST_AGENT_ID[3]}" = "e2e-a9" ] &&
        [ "${GUEST_SSH_PORT[3]}" = "9504" ] &&
        [ "${GUEST_AGENT_PORT[3]}" = "9474" ] &&
        [ "${GUEST_MEM_MB[3]}" = "2048" ]; then
        ok "四台 guest 就位:vma=AlmaLinux 9 / e2e-a9 / 9504+9474 / 2048MB"
    else
        bad "guest 事实表不是预期四台(vma 缺失或字段不对)"
    fi
    case "${GUEST_IMAGE[3]}" in
        AlmaLinux-9-GenericCloud-*.qcow2) ok "vma 基础镜像钉了 AlmaLinux 9 日期版本:${GUEST_IMAGE[3]}" ;;
        *) bad "vma 基础镜像名不对:${GUEST_IMAGE[3]}" ;;
    esac
    case "${GUEST_IMAGE_URL[3]}" in
        https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/*) ok "vma 镜像 URL 指向 Alma 官方 cloud 目录" ;;
        *) bad "vma 镜像 URL 不对:${GUEST_IMAGE_URL[3]}" ;;
    esac
    if [ "${GUEST_MEM_MB[0]}" = "1024" ] && [ "${GUEST_MEM_MB[1]}" = "1024" ] &&
        [ "${GUEST_MEM_MB[2]}" = "1024" ]; then
        ok "前三台仍各 1024MB(行为不变)"
    else
        bad "前三台内存被改动"
    fi
    for i in "${!GUEST_NAMES[@]}"; do
        echo "       ${GUEST_NAMES[i]}: family=${GUEST_FAMILY[i]} agent_id=${GUEST_AGENT_ID[i]} mem=${GUEST_MEM_MB[i]}MB ssh=${GUEST_SSH_PORT[i]} agent=${GUEST_AGENT_PORT[i]}"
    done

    step "cloud-init / seed 渲染"
    out="$(fleet_render_user_data 0 "ssh-ed25519 AAAA-selftest daedalus-fleet-kvm")"
    case "$out" in
        "#cloud-config"*) ok "user-data 以 #cloud-config 开头" ;;
        *) bad "user-data 缺 #cloud-config 头" ;;
    esac
    case "$out" in
        *"hostname: vmu"*) ok "user-data 写对 hostname(vmu)" ;;
        *) bad "user-data hostname 不对" ;;
    esac
    case "$out" in
        *"ssh-ed25519 AAAA-selftest"*) ok "user-data 带上 ssh 公钥" ;;
        *) bad "user-data 缺 ssh 公钥" ;;
    esac
    case "$out" in
        *"NOPASSWD:ALL"*) ok "user-data 给免密 sudo" ;;
        *) bad "user-data 缺免密 sudo" ;;
    esac
    case "$out" in
        *"groups: [sudo]"*) ok "Ubuntu guest 落进 sudo 组" ;;
        *) bad "Ubuntu guest 的组不对" ;;
    esac
    case "$(fleet_render_user_data 2 "k")" in
        *"groups: [wheel]"*) ok "CentOS guest 落进 wheel 组" ;;
        *) bad "CentOS guest 的组不对" ;;
    esac
    case "$(fleet_render_user_data 3 "k")" in
        *"groups: [wheel]"*) ok "AlmaLinux guest(vma)落进 wheel 组" ;;
        *) bad "AlmaLinux guest(vma)的组不对" ;;
    esac
    case "$(fleet_render_user_data 3 "k")" in
        *"packages: [sudo]"*) ok "AlmaLinux guest(vma)预装 sudo(精简云镜像可能没带)" ;;
        *) bad "AlmaLinux guest(vma)没预装 sudo" ;;
    esac
    case "$(fleet_render_meta_data 1)" in
        *"instance-id: vmd-001"*) ok "meta-data 的 instance-id 正确" ;;
        *) bad "meta-data 的 instance-id 不对" ;;
    esac
    case "$(fleet_render_agent_config 0)" in
        *"agent_id: e2e-u24"*) ok "config.yaml 带 vmu 自己的 agent_id" ;;
        *) bad "config.yaml 的 agent_id 不对" ;;
    esac
    case "$(fleet_render_agent_config 2)" in
        *"agent_id: e2e-c9"*) ok "config.yaml 带 vmc 自己的 agent_id" ;;
        *) bad "config.yaml 的 agent_id 不对(vmc)" ;;
    esac
    case "$(fleet_render_agent_config 3)" in
        *"agent_id: e2e-a9"*) ok "config.yaml 带 vma 自己的 agent_id" ;;
        *) bad "config.yaml 的 agent_id 不对(vma)" ;;
    esac
    for k in cert_path key_path ca_path ca_key_path; do
        case "$(fleet_render_agent_config 0)" in
            *"$k: /etc/daedalus-agent/tls/"*) ok "config.yaml 的 $k 非空(agent 才开 mTLS)" ;;
            *) bad "config.yaml 的 $k 不对" ;;
        esac
    done
    if [ "$(fleet_render_agent_config 0)" != "$(fleet_render_agent_config 1)" ]; then
        ok "各台 guest 的 config.yaml 各自不同(agent_id 不串台)"
    else
        bad "各台 guest 的 config.yaml 相同 —— agent_id 串台了"
    fi

    step "qemu 命令行渲染(hostfwd / overlay / seed)"
    out="$(fleet_render_qemu_args 0 "/tmp/vmu")"
    case "$out" in
        *"hostfwd=tcp:127.0.0.1:9501-:22"*) ok "vmu 的 ssh 端口转发已渲染" ;;
        *) bad "vmu 的 ssh hostfwd 没渲染对" ;;
    esac
    case "$out" in
        *"hostfwd=tcp:127.0.0.1:9471-:9443"*) ok "vmu 的 agent 端口转发已渲染" ;;
        *) bad "vmu 的 agent hostfwd 没渲染对" ;;
    esac
    case "$out" in
        *"file=/tmp/vmu/overlay.qcow2"*) ok "overlay 挂在 virtio 上" ;;
        *) bad "overlay 路径没渲染对" ;;
    esac
    case "$out" in
        *"seed.iso,media=cdrom"*) ok "NoCloud seed 以 cdrom 挂载" ;;
        *) bad "seed 挂载方式不对" ;;
    esac
    case "$out" in
        *"-daemonize"*) ok "qemu 以 daemon 方式起(有 pidfile)" ;;
        *) bad "qemu 没以 daemon 方式起" ;;
    esac
    # 回归钉子:"-name vmu" 曾整行当一个 argv,qemu 直接 `invalid option` 拒启。
    if printf '%s\n' "$out" | grep -q '[[:space:]]'; then
        bad "qemu 参数里有空格 —— 一行必须只放一个 argv 元素"
    else
        ok "qemu 参数一行一个 argv(没有被空格粘成非法选项)"
    fi
    if [ "$(printf '%s\n' "$out" | grep -A1 -x -- '-name' | tail -1)" = "vmu" ]; then
        ok "-name 与 guest 名分属两个 argv(顺序正确)"
    else
        bad "-name 后面跟的不是独立的 vmu"
    fi
    if [ "$(printf '%s\n' "$out" | grep -c '^-')" = "14" ]; then
        ok "qemu 选项数符合预期(14 个 - 开头元素)"
    else
        bad "qemu 选项数异常(实际 $(printf '%s\n' "$out" | grep -c '^-'))"
    fi
    # vma(RHEL 9 行临时验证宿主):内存与端口必须逐台渲染对。
    out="$(fleet_render_qemu_args 3 "/tmp/vma")"
    case "$out" in
        *"hostfwd=tcp:127.0.0.1:9504-:22"*) ok "vma 的 ssh 端口转发已渲染(9504)" ;;
        *) bad "vma 的 ssh hostfwd 没渲染对" ;;
    esac
    case "$out" in
        *"hostfwd=tcp:127.0.0.1:9474-:9443"*) ok "vma 的 agent 端口转发已渲染(9474)" ;;
        *) bad "vma 的 agent hostfwd 没渲染对" ;;
    esac
    if [ "$(printf '%s\n' "$out" | grep -A1 -x -- '-m' | tail -1)" = "2048" ]; then
        ok "vma 的 -m 是 2048(RHEL 9 验收门槛 ≥2 GB)"
    else
        bad "vma 的 -m 不是 2048(实际 $(printf '%s\n' "$out" | grep -A1 -x -- '-m' | tail -1))"
    fi
    # 前三台的内存不许被顺带改掉。
    if [ "$(fleet_render_qemu_args 0 "/tmp/vmu" | grep -A1 -x -- '-m' | tail -1)" = "1024" ] &&
        [ "$(fleet_render_qemu_args 1 "/tmp/vmd" | grep -A1 -x -- '-m' | tail -1)" = "1024" ] &&
        [ "$(fleet_render_qemu_args 2 "/tmp/vmc" | grep -A1 -x -- '-m' | tail -1)" = "1024" ]; then
        ok "vmu/vmd/vmc 的 -m 仍是 1024(前三台行为不变)"
    else
        bad "前三台的 -m 被改动"
    fi

    step "包管理器撞锁的重试语义(失败不能被吞成成功)"
    # 先确认被测函数真的在(否则"命令找不到"会被当成"判定为假",测试假通过)。
    if command -v fleet_remote_pkg_retry >/dev/null 2>&1 \
        && command -v fleet_remote_guest_prep_needed >/dev/null 2>&1; then
        ok "被测函数(重试 / prep 门槛)确实已定义"
    else
        bad "被测函数缺失 —— 下面的断言会假通过"
    fi
    # 桩:前两次 ssh 失败(模拟 apt-daily 抢锁),第三次成功。
    saved_ssh_fn="$(declare -f fleet_remote_ssh || true)"
    saved_sleep_fn="$(declare -f sleep || true)"
    # 计数器必须落在文件里:fleet_remote_pkg_retry 用命令替换跑 ssh,那是个子
    # shell,普通变量加不回来。
    pkg_count_file="$tmp/pkg_calls"
    : > "$pkg_count_file"
    fleet_remote_ssh() {
        echo x >> "$pkg_count_file"
        if [ "$(wc -l < "$pkg_count_file")" -lt 3 ]; then
            printf 'E: Could not get lock /var/lib/apt/lists/lock\n' >&2
            return 100
        fi
        return 0
    }
    sleep() { :; }
    if fleet_remote_pkg_retry vmu "apt-get update" true >/dev/null 2>&1 \
        && [ "$(wc -l < "$pkg_count_file")" -eq 3 ]; then
        ok "前两次撞锁后第 3 次成功(重试而不是放弃)"
    else
        bad "撞锁重试不对(调用 $(wc -l < "$pkg_count_file") 次)"
    fi
    : > "$pkg_count_file"
    fleet_remote_ssh() {
        echo x >> "$pkg_count_file"
        return 100
    }
    if fleet_remote_pkg_retry vmu "apt-get update" true >/dev/null 2>&1; then
        bad "始终失败的命令居然被判成成功"
    else
        ok "始终失败时最终判失败(不会被静默放过;重试 $(wc -l < "$pkg_count_file") 次)"
    fi

    # prep 门槛:标记(本台是否 prep 过)+ 现状(目标状态是否被破坏)。
    saved_fleet_work="${FLEET_WORK:-}"
    FLEET_WORK="$tmp/fleet"
    mkdir -p "$FLEET_WORK/vms/vmu" "$FLEET_WORK/vms/vmc"

    # 全新 guest(Ubuntu 云镜像自带 rsync)也必须做一次 prep:否则 apt 索引
    # 是出厂快照,后面装包会撞"没有候选版本"。这是真机上踩到的缺口。
    fleet_remote_ssh() { return 0; }
    if fleet_remote_guest_prep_needed 0; then
        ok "全新 guest 即使自带 rsync 也要 prep 一次(刷新旧索引)"
    else
        bad "全新 guest 被误判为已 prep —— apt 索引会一直是出厂快照"
    fi

    : > "$FLEET_WORK/vms/vmu/.prepped"
    if fleet_remote_guest_prep_needed 0; then
        bad "已 prep 且 rsync 在位却仍判定需要装包"
    else
        ok "已 prep + rsync 在位 → 跳过装包"
    fi
    fleet_remote_ssh() { return 1; }
    if fleet_remote_guest_prep_needed 0; then
        ok "已 prep 但 rsync 没了 → 判定需要补装"
    else
        bad "rsync 缺失却没判定需要补装"
    fi
    : > "$FLEET_WORK/vms/vmc/.prepped"
    fleet_remote_ssh() { return 0; }
    if fleet_remote_guest_prep_needed 2; then
        bad "vmc 已 prep 且无 rsync/btrfs 却仍判定需要装包"
    else
        ok "vmc 已 prep 且无 rsync/btrfs → 跳过装包(SNAPSHOT_CAPABILITY_NONE 成立)"
    fi
    if [ -n "$saved_fleet_work" ]; then FLEET_WORK="$saved_fleet_work"; else unset FLEET_WORK; fi
    if [ -n "$saved_ssh_fn" ]; then eval "$saved_ssh_fn"; else unset -f fleet_remote_ssh; fi
    if [ -n "$saved_sleep_fn" ]; then eval "$saved_sleep_fn"; else unset -f sleep; fi

    step "宿主 ~/.ssh/config 标记块的幂等性"
    block="$(mktemp)"
    fleet_render_ssh_config_block "/home/tester" "/home/tester/keys/id_ed25519" > "$block"
    printf 'Host keepme\n    User someone\n' > "$tmp/ssh_config"
    fleet_upsert_ssh_config "$tmp/ssh_config" "$block"
    cp "$tmp/ssh_config" "$tmp/once"
    fleet_upsert_ssh_config "$tmp/ssh_config" "$block"
    if cmp -s "$tmp/once" "$tmp/ssh_config"; then
        ok "写两次结果逐字节相同(幂等)"
    else
        bad "写两次结果不同 —— 标记块 upsert 不幂等"
    fi
    if [ "$(grep -c -F "$FLEET_SSH_BEGIN" "$tmp/ssh_config")" = "1" ]; then
        ok "标记块只出现一次(没有叠加)"
    else
        bad "标记块出现次数不是 1"
    fi
    if grep -q "Host keepme" "$tmp/ssh_config"; then
        ok "用户原有的 ssh 配置内容被保留"
    else
        bad "用户原有的 ssh 配置内容被弄丢了"
    fi
    if grep -q "Host vmu" "$tmp/ssh_config" && grep -q "Host vmd" "$tmp/ssh_config" &&
        grep -q "Host vmc" "$tmp/ssh_config" && grep -q "Host vma" "$tmp/ssh_config"; then
        ok "标记块里有 vmu/vmd/vmc/vma 四个 Host 条目"
    else
        bad "标记块内容不全"
    fi
    fleet_remove_ssh_config_block "$tmp/ssh_config"
    if grep -q -F "$FLEET_SSH_BEGIN" "$tmp/ssh_config"; then
        bad "移除标记块失败"
    else
        ok "标记块可整块移除(销毁用)"
    fi
    if grep -q "Host keepme" "$tmp/ssh_config"; then
        ok "移除标记块不动用户原有内容"
    else
        bad "移除标记块误伤了用户原有内容"
    fi

    step "干跑边界:不 ssh、不碰 KVM、不联网"
    local stubdir="$tmp/stub" marker="$tmp/touched"
    mkdir -p "$stubdir"
    for t in ssh curl qemu-img qemu-system-x86_64 cloud-localds openssl; do
        printf '#!/bin/sh\ntouch "%s"\necho "selftest 不该调用 %s" >&2\nexit 97\n' "$marker" "$t" \
            > "$stubdir/$t"
        chmod +x "$stubdir/$t"
    done
    # 用 DAEDALUS_SELFTEST_PROBE 打断递归:内层只跑断言,不再套一层桩测试。
    if [ "${DAEDALUS_SELFTEST_PROBE:-0}" = "1" ]; then
        ok "内层探测运行(跳过硬跑边界自检,避免递归)"
    elif PATH="$stubdir:$PATH" DAEDALUS_SELFTEST_PROBE=1 \
        "${BASH:-bash}" "$SELF" --selftest >/dev/null 2>&1; then
        if [ -e "$marker" ]; then
            bad "selftest 期间调用了 ssh/qemu/网络类命令"
        else
            ok "selftest 期间未调用 ssh/qemu/网络类命令(桩文件未被触发)"
        fi
    else
        bad "桩环境下重跑 selftest 失败(说明 selftest 依赖了本不该依赖的外部命令)"
    fi

    rm -rf "$tmp" "$block"
}

# ================================================================== 入口 ====

MODE=""
PURGE=0

fleet_parse_args "$@" || exit 2

case "$MODE" in
    selftest)
        run_selftest
        trap - ERR
        echo
        echo "provision-kvm-hosts: $pass 通过 / $fail 失败"
        [ "$fail" -eq 0 ]
        ;;
    up|status|destroy)
        trap - ERR
        case "$FLEET_DIR_NAME" in
            *[!A-Za-z0-9._-]*|'') echo "工作目录名不合法:$FLEET_DIR_NAME" >&2; exit 2 ;;
        esac
        if ! "$SSH" -o BatchMode=yes -o ConnectTimeout=15 "$KVM_HOST" true; then
            echo "ssh $KVM_HOST 不通(BatchMode);检查 ~/.ssh/config 与该别名" >&2
            exit 1
        fi
        if [ "$MODE" = "destroy" ]; then
            run_remote destroy "$PURGE"
        else
            run_remote "$MODE"
        fi
        ;;
    *)
        usage
        exit 2
        ;;
esac
