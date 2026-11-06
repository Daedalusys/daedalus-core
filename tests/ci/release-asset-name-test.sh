#!/usr/bin/env bash
# release_asset_name 的表驱动自检。
#
# 为什么单独成文件:资产命名与 2GiB 上限守卫是发布腿的第一道闸。它错了只会在
# 一次完整出货 run 上暴露 —— 那时已经烧掉几十分钟的镜像构建与两条 KVM 门禁,
# 而"Release 里有资产"还会是个假象(上传被静默跳过成功)。被测函数无副作用,
# 故用表驱动把每个分支钉死,与 workflow-hygiene.sh 的 st() 同型。
#
# 用法: bash tests/ci/release-asset-name-test.sh
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/../../scripts/release-asset-name.sh"

PASS_N=0
FAIL_N=0
st() { # st <期望> <实际> <说明>
    if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"; PASS_N=$((PASS_N + 1))
    else printf '  FAIL  %s (期望 %q,实际 %q)\n' "$3" "$1" "$2"; FAIL_N=$((FAIL_N + 1)); fi
}

# 被测命令的薄壳:用 `bash` 调用,故脚本不需要可执行位就能被本测试覆盖。
release_asset_name() { bash "$SCRIPT" "$@"; }

# run <命令…> —— 执行并捕获 stdout 到 $out、退出码到 $rc。
out=""
rc=0
run() { out="$("$@" 2>/dev/null)"; rc=$?; }

printf '== release-asset-name-test.sh ==\n'

if [ ! -f "$SCRIPT" ]; then
    printf '  FAIL  被测脚本不存在: %s\n' "$SCRIPT"
    printf 'RESULT: SELFTEST FAIL (1)\n'
    exit 1
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
: >"$TMP/disk.qcow2"
: >"$TMP/bootc-almalinux-10-generic-iso-x86_64.iso"
# 稀疏文件:stat 报 2.2GB、du 报 0,免得真写 2.2GB 磁盘把开发机填满。
truncate -s 2200000000 "$TMP/big.qcow2"
# 恰好等于 2GiB(2147483648)不算"超过",守卫的边界值由本用例钉住。
truncate -s 2147483648 "$TMP/exact.qcow2"

SHA40=0123456789abcdef0123456789abcdef01234567

# --- 情形 1:RELEASE_TAG 非空 → 用 tag ---
RELEASE_TAG=v0.4.0 GITHUB_SHA="$SHA40" run release_asset_name qcow2 "$TMP/disk.qcow2"
st "qcow2-v0.4.0.qcow2" "$out" "tag 非空 → 用 tag 作版本"

# --- 情形 2:RELEASE_TAG 为空 → 用 commit 短 sha(12 位) ---
RELEASE_TAG= GITHUB_SHA="$SHA40" run release_asset_name qcow2 "$TMP/disk.qcow2"
st "qcow2-0123456789ab.qcow2" "$out" "tag 为空 → 用短 sha 前 12 位"

# --- 情形 3:ISO 的 ext 由路径后缀判定,与 kind 无关 ---
RELEASE_TAG=v0.4.0 GITHUB_SHA="$SHA40" \
    run release_asset_name iso "$TMP/bootc-almalinux-10-generic-iso-x86_64.iso"
st "iso-v0.4.0.iso" "$out" "iso 的 ext 由后缀判定"

# --- 情形 4:超过 2GiB → 退出码 2 且不打印文件名 ---
RELEASE_TAG=v0.4.0 GITHUB_SHA="$SHA40" run release_asset_name qcow2 "$TMP/big.qcow2"
st 2 "$rc" "超 2GiB → 退出码 2"
st "" "$out" "超 2GiB → 不打印文件名(不得静默放行)"

# --- 情形 5:恰好 2GiB → 允许(上限是"超过才拒") ---
RELEASE_TAG=v0.4.0 GITHUB_SHA="$SHA40" run release_asset_name qcow2 "$TMP/exact.qcow2"
st 0 "$rc" "恰好 2GiB → 退出码 0"
st "qcow2-v0.4.0.qcow2" "$out" "恰好 2GiB → 正常输出资产名"

# --- 情形 6:参数缺失 → 退出码 1 ---
run release_asset_name
st 1 "$rc" "两参数都缺 → 退出码 1"
run release_asset_name qcow2
st 1 "$rc" "缺 path → 退出码 1"

# --- 情形 7:两个版本来源都缺 → 退出码 1,不得产出无版本的资产名 ---
out="$(env -u RELEASE_TAG -u GITHUB_SHA bash "$SCRIPT" qcow2 "$TMP/disk.qcow2" 2>/dev/null)"
st 1 "$?" "RELEASE_TAG 与 GITHUB_SHA 均缺 → 退出码 1"

if [ "$FAIL_N" -eq 0 ]; then
    printf 'RESULT: SELFTEST PASS\n'
    exit 0
fi
printf 'RESULT: SELFTEST FAIL (%d)\n' "$FAIL_N"
exit 1
