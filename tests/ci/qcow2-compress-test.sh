#!/usr/bin/env bash
# qcow2 压缩腿自检(可跳过):压缩命令本身 + 「压缩不成就别用」的回退判据。
#
# 为什么单独成文件:这条腿的产物就是用户下载的字节(#196),它退化有两个方向,
# 静态 grep 都挡不住:
#   * `-c` 被去掉 → 发布出去的还是 1.3 GB(下载量没降),且没人会发现;
#   * 回退判据写成 `-le` 或符号反了 → 已不可压的输入上「压缩」把产物变大,
#     而那正是这条腿要消灭的东西。
#
# 判据只有一处定义:在 .github/workflows/build.yml 的压缩步骤里(函数
# qcow2_should_adopt_compressed),本脚本从那里**原样抽出**再喂输入 ——
# 判据被改坏时,这里测的就是改坏后的那份,不会与生产端漂开。
#
# qemu-img 由 qemu-utils 提供,只在 build-qcow2 那个 job 装;日常 CI 的 test
# job 没有它,故缺 qemu-img 时打 `SKIP: qemu-img 未安装` 并退出 0(不能让门禁
# 因环境缺件变红);判据部分与 qemu-img 无关,照测不误。
#
# 用法: bash tests/ci/qcow2-compress-test.sh    # 全绿打印 RESULT: SELFTEST PASS
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKFLOW="$ROOT/.github/workflows/build.yml"

PASS_N=0
FAIL_N=0
st() { # st <期望> <实际> <说明>
    if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"; PASS_N=$((PASS_N + 1))
    else printf '  FAIL  %s (期望 %q,实际 %q)\n' "$3" "$1" "$2"; FAIL_N=$((FAIL_N + 1)); fi
}

printf '== qcow2-compress-test.sh ==\n'

if [ ! -f "$WORKFLOW" ]; then
    printf '  FAIL  找不到 %s\n' "$WORKFLOW"
    printf 'RESULT: SELFTEST FAIL (1)\n'
    exit 1
fi

# --- 1. 把回退判据从 build.yml 原样抽出来(判据只有一处定义) ---
# 用整行标记取块:标记行本身在 workflow 里是注释,抽取时按正则去掉前后空白,
# 缩进无关(YAML 的 run 块整体缩进 10 空格,eval 同样能吃)。
extract_guard() {
    awk '
        /^[[:space:]]*# BEGIN qcow2-adopt-guard[[:space:]]*$/ { on = 1; next }
        on && /^[[:space:]]*# END qcow2-adopt-guard[[:space:]]*$/ { exit }
        on { print }
    ' "$WORKFLOW"
}

GUARD="$(extract_guard)"
st 1 "$([ -n "$GUARD" ] && echo 1 || echo 0)" "从 build.yml 抽得出回退判据块"

if [ -n "$GUARD" ]; then
    eval "$GUARD"
fi
st 1 "$(declare -F qcow2_should_adopt_compressed >/dev/null 2>&1 && echo 1 || echo 0)" \
    "判据是已定义函数 qcow2_should_adopt_compressed"

# --- 2. 回退判据的两种情形:变小才采用,不变/变大一律保留原件 ---
# 「输出不小于输入时不采用」是这条腿的核心保护:压缩绝不能让产物变大。
adopt_or_keep() { # <orig_bytes> <new_bytes>
    if qcow2_should_adopt_compressed "$1" "$2"; then echo adopt; else echo keep; fi
}
st "adopt" "$(adopt_or_keep 1000 999)" "压缩后更小 → 采用压缩件"
st "keep" "$(adopt_or_keep 1000 1000)" "压缩后字节数相等 → 保留原件(绝不让产物变大)"
st "keep" "$(adopt_or_keep 1000 1001)" "压缩后更大 → 保留原件(绝不让产物变大)"
st "keep" "$(adopt_or_keep 0 0)" "0 字节 → 0 字节 → 保留原件"

# --- 3. workflow 的静态不变式(只看非注释行:注释里写着 -c 不算数) ---
live_lines="$(awk '!/^[[:space:]]*#/ && !/^[[:space:]]*$/' "$WORKFLOW")"

has_live() { printf '%s\n' "$live_lines" | grep -Fq -- "$1"; }
st 1 "$(has_live 'qemu-img convert -O qcow2 -c' && echo 1 || echo 0)" \
    "压缩步骤用 qemu-img convert -O qcow2 -c(带 -c 才是 zlib 压缩簇)"
st 1 "$(has_live 'qemu-img check' && echo 1 || echo 0)" \
    "压缩后有 qemu-img check 断言(完整性不验等于没验)"
st 1 "$(has_live 'qcow2_should_adopt_compressed' && echo 1 || echo 0)" \
    "workflow 里真的调用了那个判据(不是只定义不用)"
st 1 "$(has_live '::warning::' && echo 1 || echo 0)" \
    "压缩没变小这条分支会打 ::warning::(回退不是静默发生)"

# 压缩必须在 artifact 上传之前:上传/引导/晋级三步取的都是上传的那份字节。
line_of() { grep -n -- "$1" "$WORKFLOW" 2>/dev/null | head -1 | cut -d: -f1; }
compress_line="$(line_of 'name: Compress qcow2')"
upload_line="$(line_of 'name: Upload qcow2 artifact')"
st 1 "$([ -n "$compress_line" ] && [ -n "$upload_line" ] && [ "$compress_line" -lt "$upload_line" ] && echo 1 || echo 0)" \
    "压缩步骤排在 artifact 上传之前(发出去的是压缩后的字节)"

# --- 4. 真跑一遍压缩(qemu-img 在才跑;缺件则跳过,不得让门禁变红) ---
if ! command -v qemu-img >/dev/null 2>&1; then
    printf 'SKIP: qemu-img 未安装\n'
else
    TMP="$(mktemp -d)"
    trap 'rm -rf "$TMP"' EXIT

    # 64 MiB 高度可压缩的源数据。用 raw → qcow2 造,只依赖 qemu-img;
    # 全零会被 qcow2 当零簇跳过(不写实簇),压缩比就测不出东西,故填 'A'。
    dd if=/dev/zero bs=1M count=64 status=none | tr '\0' 'A' >"$TMP/raw.img"
    if qemu-img convert -O qcow2 "$TMP/raw.img" "$TMP/src.qcow2" 2>"$TMP/mk.err"; then mk_rc=0; else mk_rc=$?; fi
    st 0 "$mk_rc" "造出 64MiB 可压缩的源 qcow2"

    in_size="$(stat -c %s "$TMP/src.qcow2" 2>/dev/null || echo 0)"

    # 与 workflow 里**同一条**压缩命令(qemu-img convert -O qcow2 -c)。
    if qemu-img convert -O qcow2 -c "$TMP/src.qcow2" "$TMP/out.qcow2" 2>"$TMP/conv.err"; then conv_rc=0; else conv_rc=$?; fi
    st 0 "$conv_rc" "qemu-img convert -O qcow2 -c 退出 0"

    fmt="$(qemu-img info "$TMP/out.qcow2" 2>/dev/null | awk -F': ' '/^file format:/{print $2; exit}')"
    st "qcow2" "$fmt" "输出仍是 qcow2 格式"

    if qemu-img check "$TMP/out.qcow2" >"$TMP/check.out" 2>&1; then chk_rc=0; else chk_rc=$?; fi
    st 0 "$chk_rc" "qemu-img check 通过"

    out_size="$(stat -c %s "$TMP/out.qcow2" 2>/dev/null || echo 0)"
    st 1 "$([ "$out_size" -gt 0 ] && [ "$out_size" -lt "$in_size" ] && echo 1 || echo 0)" \
        "压缩后字节数小于输入(${in_size} → ${out_size})"

    # 判据在这组真实数字上的表现:变小 → 采用。
    st "adopt" "$(adopt_or_keep "$in_size" "$out_size")" "真实压缩结果更小 → 采用压缩件"
fi

printf 'PASS=%d FAIL=%d\n' "$PASS_N" "$FAIL_N"
if [ "$FAIL_N" -eq 0 ]; then
    printf 'RESULT: SELFTEST PASS\n'
    exit 0
fi
printf 'RESULT: SELFTEST FAIL (%d)\n' "$FAIL_N"
exit 1
