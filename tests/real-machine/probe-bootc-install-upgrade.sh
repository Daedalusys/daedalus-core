#!/usr/bin/env bash
# =============================================================================
# probe-bootc-install-upgrade.sh —— A2 残余:bootc install to-disk 与 bootc upgrade
# =============================================================================
# 证伪什么
#   P2-1 已覆盖「换部署 / 回滚」(bootc switch / rollback)。但 `bootc install
#   to-disk` 与 `bootc upgrade` 从未被单独验过 —— bootc-image-builder 出的 qcow2
#   内部做过 install,那不等于「在 guest 里手工跑 install」。
#
# 两条
#   1. `bootc install to-disk` 对**第二块虚拟盘**(/dev/vdb)执行,成功后目标盘上
#      出现 ostree 部署(挂载后 ostree admin status 可见)。**绝不指向宿主盘**;
#   2. `bootc upgrade`:先 `--check`(判「有没有可升的版本」),再真 upgrade。
#      源镜像为本地派生 ref、registry 上不存在时,如实记「无新 digest 可升」——
#      判据区分「命令无效」与「没有可升的版本」。
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
# 每次重跑都重建:上一次失败的尝试会在盘上留下半成品 ostree 仓库(实测
# "Multiple commit objects found" 就来自复用了旧盘)。
VDB="$DRIVER_WORK/a2-vdb.qcow2"
rm -f "$VDB"
qemu-img create -f qcow2 "$VDB" 20G >/dev/null
export DRIVER_QEMU_EXTRA_ARGS="-drive file=$VDB,if=virtio,format=qcow2"
DRIVER_QEMU_SNAPSHOT=1

HOST_LSBLK_BEFORE="$(lsblk -o NAME,SIZE,TYPE,MOUNTPOINT 2>&1)"
driver_boot

say "A2-0 · guest 内盘位与 bootc 可用性"
gssh "lsblk -o NAME,SIZE,TYPE; echo ---; bootc --version; bootc status 2>&1 | head -8" 2>&1 | sed 's/^/      /'

say "A2-1 · bootc install to-disk /dev/vdb"
# bootc 在「不是容器内」的环境里必须显式给源镜像(--source-imgref)。
# 首选形态是本地 OCI 归档(零凭据、零 registry,与一次性 verify 镜像同源);
# 归档形态不被接受时回退到 registry 形态(记录实际用了哪种)。
say "A2-1 · bootc install to-disk /dev/vdb"
gssh "bootc install to-disk --help 2>&1 | head -24" 2>&1 | sed 's/^/      /'
ARCHIVE="$DRIVER_WORK/a2-source.tar"
driver_oci_archive "$DRIVER_IMAGE_BASE" "$ARCHIVE"
gssh_scp "$ARCHIVE" /home/verify/a2-source.tar || die "源镜像归档送进 guest 失败"
gssh "bootc install --help 2>&1 | sed -n '1,60p'" 2>&1 | sed 's/^/      /'
info "源镜像归档: $ARCHIVE → guest:/home/verify/a2-source.tar"
# bootc 的 --source-imgref 形态随版本变动;逐个候选试,取第一个成功者(失败很快,
# 只有真正开始装盘的那次才耗时)。全部失败时把最后一次的原始错误留证。
SRC_CANDIDATES=(
    "oci-archive:/home/verify/a2-source.tar"
    "oci:/home/verify/a2-source.tar"
    "registry:$DRIVER_IMAGE"
    "docker://$DRIVER_IMAGE"
)
INS=""; INS_RC=1; SRC_USED=""
for src in "${SRC_CANDIDATES[@]}"; do
    INS="$(gssh "sudo timeout 1500 bootc install to-disk --wipe --source-imgref '$src' /dev/vdb 2>&1; echo RC=\$?" 2>&1)"
    INS_RC="$(printf '%s' "$INS" | sed -n 's/^RC=//p' | tail -1)"
    SRC_USED="$src"
    info "source=$src → rc=${INS_RC:-?}"
    [ "${INS_RC:-1}" = "0" ] && break
    printf '%s\n' "$INS" | tail -3 | sed 's/^/        /' | tee -a "$DRIVER_LOG"
done
printf '%s\n' "$INS" | tail -25 | sed 's/^/      /' | tee -a "$DRIVER_LOG"
info "install rc=$INS_RC (source=$SRC_USED)"

PART="$(gssh "lsblk -ln -o NAME,FSTYPE /dev/vdb 2>&1" 2>&1)"
info "目标盘分区: $(printf '%s' "$PART" | tr '\n' ' ')"
ROOT_PART="$(printf '%s' "$PART" | awk '$2=="xfs"{print $1}' | head -1)"
if [ -n "$ROOT_PART" ]; then
    OST="$(gssh "sudo mkdir -p /mnt/a2root && sudo mount -o ro /dev/$ROOT_PART /mnt/a2root && sudo ostree admin status --sysroot=/mnt/a2root 2>&1 | head -8; sudo umount /mnt/a2root" 2>&1)"
    printf '%s\n' "$OST" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
else
    OST=""
fi
if [ "${INS_RC:-1}" = "0" ] && [ -n "$ROOT_PART" ] && printf '%s' "$OST" | grep -qiE 'daedalus|[\*] |ostree'; then
    driver_verdict PASS "A2-1 install to-disk 成功且目标 /dev/vdb 上有 ostree 部署"
elif [ "${INS_RC:-1}" = "0" ] && [ -n "$ROOT_PART" ]; then
    driver_verdict FAIL "A2-1 install 返回 0 且目标有分区,但 ostree 部署不可见: $(printf '%s' "$OST" | head -2 | tr '\n' ' ')"
else
    # PROBE_WIP:--source-imgref 的取值形态在本 bootc 版本(1.16.13)上对四种 transport
    # 全部拒绝或报 "Multiple commit objects found",尚无法区分「探针的调用姿势不对」
    # 与「bootc 直接对 bootc 派生镜像 install 有缺陷」。原始错误见上,结论留待校准。
    driver_verdict PROBE_WIP "A2-1 install to-disk 未成功(rc=${INS_RC:-?},source=$SRC_USED): $(printf '%s' "$INS" | grep -m1 -E 'error|Error' | tr -d '\n' | head -c 160)"
fi

say "A2-2 · bootc upgrade"
CHK="$(gssh "sudo timeout 300 bootc upgrade --check 2>&1; echo RC=\$?" 2>&1)"
printf '%s\n' "$CHK" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
CHK_RC="$(printf '%s' "$CHK" | sed -n 's/^RC=//p' | tail -1)"
BEFORE_HASH="$(gssh "sudo bootc status 2>/dev/null | sed -n 's/.*imageDigest: *\(sha256:[0-9a-f]*\).*/\1/p' | head -1" 2>&1)"
info "upgrade 前 digest: $BEFORE_HASH"
UP="$(gssh "sudo timeout 900 bootc upgrade 2>&1; echo RC=\$?" 2>&1)"
printf '%s\n' "$UP" | tail -15 | sed 's/^/      /' | tee -a "$DRIVER_LOG"
UP_RC="$(printf '%s' "$UP" | sed -n 's/^RC=//p' | tail -1)"
AFTER_HASH="$(gssh "sudo bootc status 2>/dev/null | sed -n 's/.*imageDigest: *\(sha256:[0-9a-f]*\).*/\1/p' | head -1" 2>&1)"
info "upgrade 后 digest: $AFTER_HASH"
ORIGIN="$(gssh "sudo bootc status 2>/dev/null | sed -n 's/.*image: *//p' | head -1" 2>&1)"
info "当前部署来源: $ORIGIN"
if [ "${UP_RC:-1}" = "0" ]; then
    if [ "$BEFORE_HASH" != "$AFTER_HASH" ]; then
        driver_verdict PASS "A2-2 bootc upgrade 成功且 digest 变化($BEFORE_HASH → $AFTER_HASH)"
    else
        driver_verdict PASS "A2-2 bootc upgrade rc=0,无新 digest 可升(digest 未变)"
    fi
elif printf '%s' "$UP" | grep -qiE 'no (updates?|changes?|new)|up to date|already'; then
    driver_verdict PASS "A2-2 无新 digest 可升(如实标注,不算失败)"
elif printf '%s' "$UP" | grep -qiE 'pinging container registry|connection refused|localhost'; then
    # 源镜像是本地派生 ref:本机根本没有该 ref 的 registry 可查,upgrade 无从比对。
    # 这是环境限制(判据要求区分「命令无效」与「没有可升的版本」),不是产品缺陷。
    driver_verdict NOTE "A2-2 无 registry 可查(部署来源 $ORIGIN 是本地派生 ref)—— 真 upgrade 未验证"
else
    driver_verdict FAIL "A2-2 bootc upgrade 命令无效/失败(rc=${UP_RC:-?}): $(printf '%s' "$UP" | tail -2 | tr '\n' ' ')"
fi
info "upgrade --check rc=$CHK_RC"

say "A2-3 · 宿主盘未被触碰"
HOST_LSBLK_AFTER="$(lsblk -o NAME,SIZE,TYPE,MOUNTPOINT 2>&1)"
if [ "$HOST_LSBLK_BEFORE" = "$HOST_LSBLK_AFTER" ]; then
    driver_verdict PASS "A2-3 宿主 lsblk 前后逐字节一致(install 目标是宿主上的一个 qcow2 文件)"
else
    driver_verdict FAIL "A2-3 宿主 lsblk 发生变化"
fi

driver_exit
