#!/usr/bin/env bash
# 「晋级到 Release」两条发布腿的行为自检。
#
# 为什么必须有:发布腿跑在两个会执行构建产物的 runner 上、且只在出货档触发,它出错的
# 代价是「Release 里躺着一个没通过验证的产物」。这种事不会有人天天看,所以每条失败路径
# 都得在这里钉死,不能只靠"上一次实跑是绿的"。
#
# 覆盖:
#   静态 —— 发布步骤只在**前面全部成功**时才跑(if 里含 success())。
#     GitHub Actions 里 if 一旦含状态检查函数,隐式的 success() 就失效;只写
#     `!cancelled()` 会让"验证判红"的产物照样被发布,而发布步骤本身显示绿 —— 假绿。
#   动态 —— 把 run 块抽出来用假 gh 驱动,钉住:产物缺失 / 超 2GiB / create 带 --target /
#     Release 已存在时的身份核对(绝不覆盖指向别的 commit 的 Release) / 并发的另一条腿
#     抢先建好 Release / 上传的文件名必须恰好是资产名 / 临时落位被清理 / 版本漂开即拦。
#
# 用法: bash tests/ci/release-promote-test.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WF="$ROOT/.github/workflows/build.yml"

PASS_N=0
FAIL_N=0
st() { # st <期望> <实际> <说明>
    if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"; PASS_N=$((PASS_N + 1))
    else printf '  FAIL  %s (期望 %q,实际 %q)\n' "$3" "$1" "$2"; FAIL_N=$((FAIL_N + 1)); fi
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

printf '== release-promote-test.sh ==\n'

if [ ! -f "$WF" ]; then
    printf '  FAIL  找不到 %s\n' "$WF"
    printf 'RESULT: PROMOTE-TEST FAIL (1)\n'
    exit 1
fi

# extract_promote <nth> <if|run> —— 取第 nth 个「Promote … to GitHub Release」步骤的
# if 值 / run 正文。按缩进走 mapping 结构,不全文 grep:注释里的 `if:` 不是步骤的 if。
# 名字要卡到 `to GitHub Release` 结尾 —— build-image 里有条 `Promote image to :latest
# and :upstream`,只匹配 `- name: Promote` 会把它抽成"第 1 个发布步骤"。
extract_promote() {
    awk -v nth="$1" -v what="$2" '
        /^      - name: / {
            # sel 必须每个步骤重算:只在"这个步骤就是第 nth 个发布步骤"时为 1,
            # 否则会把后续步骤的 run 块一路串进来(抽出的脚本尾部跑了别的 job 的脚本)。
            if ($0 ~ /^      - name: Promote .* to GitHub Release$/) { np++; sel = (np == nth) }
            else { sel = 0 }
            in_run = 0
            next
        }
        sel && what == "if" && /^        if: / { print substr($0, 13); next }
        sel && what == "run" && /^        run: \|/ { in_run = 1; next }
        sel && what == "run" && in_run {
            if ($0 == "") { print ""; next }
            if ($0 ~ /^          /) { print substr($0, 11); next }
            in_run = 0
        }
    ' "$WF"
}

QC_IF="$(extract_promote 1 if)"
ISO_IF="$(extract_promote 2 if)"
QC_SH="$TMP/promote-qcow2.sh"
ISO_SH="$TMP/promote-iso.sh"
extract_promote 1 run >"$QC_SH"
extract_promote 2 run >"$ISO_SH"

st 1 "$([ -s "$QC_SH" ] && echo 1 || echo 0)" "抽得出 qcow2 发布步骤的 run 块"
st 1 "$([ -s "$ISO_SH" ] && echo 1 || echo 0)" "抽得出 ISO 发布步骤的 run 块"
# 抽错步骤(名字前缀撞车)时脚本同样"非空" —— 必须再自证一次内容确实来自发布腿。
st 1 "$(grep -c 'release-asset-name.sh' "$QC_SH")" "qcow2 的 run 块确实来自发布步骤"
st 1 "$(grep -c 'release-asset-name.sh' "$ISO_SH")" "ISO 的 run 块确实来自发布步骤"
# 发布脚本是纯 bash:出现模板占位符就说明把后续步骤的 run 块串进来了。
st 0 "$(grep -c '\${{' "$QC_SH")" "qcow2 的 run 块未串进后续步骤(无模板占位符)"
st 0 "$(grep -c '\${{' "$ISO_SH")" "ISO 的 run 块未串进后续步骤(无模板占位符)"

# ------------------------------------------------ 静态闸:必须 success() -----
# 缺了它,前面验证判红的产物照样会被发布,而发布步骤显示绿 —— 整个计划的
# 「发布的是被验证的那一份」就不成立了。
for pair in "qcow2:$QC_IF" "iso:$ISO_IF"; do
    leg="${pair%%:*}"
    cond="${pair#*:}"
    st 1 "$(printf '%s' "$cond" | grep -c 'success()')" "$leg 发布步骤只在前面全部成功时才跑(if 含 success())"
done

# ------------------------------------------------------------- 假 gh --------
FAKEBIN="$TMP/bin"
mkdir -p "$FAKEBIN"
cat >"$FAKEBIN/gh" <<'FAKEGH'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$GH_LOG"
case "$1" in
  api) printf '%s\n' "${GH_API_SHA:-}"; exit "${GH_API_RC:-0}" ;;
  release)
    case "$2" in
      view)
        # 第一次调用决定"Release 存在与否";之后的调用模拟"另一条腿抢先建好了"
        n="$(cat "$GH_VIEW_N" 2>/dev/null || echo 0)"
        n=$((n + 1))
        printf '%s' "$n" >"$GH_VIEW_N"
        [ "$n" -ge 2 ] && exit "${GH_VIEW_RC2:-${GH_VIEW_RC:-1}}"
        exit "${GH_VIEW_RC:-1}" ;;
      create) exit "${GH_CREATE_RC:-0}" ;;
      upload)
        # 把被上传的文件各留一份副本:校验和的核心断言是「SHA256SUMS 能校验
        # 住被上传的那份字节」,所以必须真的看到文件内容,而不是数 argv。
        shift 3
        for f in "$@"; do
          case "$f" in --*) continue ;; */*) [ -f "$f" ] && cp "$f" "$GH_UPLOAD_DIR/" 2>/dev/null || true ;; esac
        done
        exit "${GH_UPLOAD_RC:-0}" ;;
      download)
        # 默认「Release 里还没有 SHA256SUMS」(rc=1);GH_PREEXISTING_SUMS 非空时
        # 模拟另一条腿已经上传过一行 —— 用来钉「追加而不是覆盖」。
        dir=""
        while [ $# -gt 0 ]; do
          case "$1" in --dir) dir="$2"; shift 2 ;; *) shift ;; esac
        done
        if [ -n "${GH_PREEXISTING_SUMS:-}" ] && [ -n "$dir" ]; then
          printf '%s\n' "$GH_PREEXISTING_SUMS" >"$dir/SHA256SUMS"
          exit 0
        fi
        exit 1 ;;
    esac ;;
esac
exit 0
FAKEGH
chmod +x "$FAKEBIN/gh"

SHA="$(git -C "$ROOT" rev-parse HEAD)"
SHORT="${SHA:0:12}"

# run_leg <qcow2|iso> [RELEASE_TAG] —— 铺一次干净工作区,跑对应 run 块,返回其退出码。
run_leg() {
    local leg="$1" tag="${2:-}" script
    if [ "$leg" = qcow2 ]; then script="$QC_SH"; else script="$ISO_SH"; fi
    rm -rf "$TMP/ws" "$TMP/iso-verify" "$TMP/runner-temp" "$TMP/gh.viewn" "$TMP/uploads"
    mkdir -p "$TMP/ws/output/qcow2" "$TMP/iso-verify/output" "$TMP/runner-temp" "$TMP/uploads"
    : >"$TMP/gh.log"
    # ISO 发布腿先读判定文件(iso-verify.sh 写的):只有 INSTALLED_LOGIN 才晋级。
    # ISO_VERDICT 可改判定;__MISSING__ 模拟「判定文件没落盘」。
    if [ "$leg" = iso ] && [ "${ISO_VERDICT:-INSTALLED_LOGIN}" != "__MISSING__" ]; then
        printf '%s\n' "${ISO_VERDICT:-INSTALLED_LOGIN}" >"$TMP/iso-verify/iso-verdict.txt"
    fi
    case "${SIZE:-0}" in
      0) ;;
      *) if [ "$leg" = qcow2 ]; then truncate -s "$SIZE" "$TMP/ws/output/qcow2/disk.qcow2"
         else truncate -s "$SIZE" "$TMP/iso-verify/output/bootc-almalinux-10-generic-iso-x86_64.iso"; fi ;;
    esac
    ( cd "$ROOT" && PATH="$FAKEBIN:$PATH" GH_LOG="$TMP/gh.log" GH_VIEW_N="$TMP/gh.viewn" \
      GITHUB_WORKSPACE="$TMP/ws" GITHUB_REPOSITORY="Daedalusys/daedalus-core" \
      GITHUB_SHA="${OVERRIDE_SHA:-$SHA}" RELEASE_TAG="$tag" VERIFY_WORK="$TMP/iso-verify" \
      RUNNER_TEMP="$TMP/runner-temp" GH_UPLOAD_DIR="$TMP/uploads" \
      bash "$script" ) >"$TMP/out.txt" 2>"$TMP/err.txt"
}
rc_of() { local rc=0; "$@" || rc=$?; printf '%s' "$rc"; }
upload_name() { grep '^release upload ' "$TMP/gh.log" | awk '{print $4}' | xargs -r basename; }
gh_calls() { wc -l <"$TMP/gh.log" | tr -d ' '; }

# --- A 产物缺失 → rc1,不静默跳过 ---
st 1 "$(GH_VIEW_RC=1 rc_of run_leg qcow2)" "qcow2 缺失 → 退出码 1(不静默跳过)"
st 1 "$(grep -c '不得在产物缺失时静默跳过' "$TMP/out.txt")" "qcow2 缺失 → 报错文案可读"
st 1 "$(GH_VIEW_RC=1 rc_of run_leg iso)" "ISO 缺失 → 退出码 1(不静默跳过)"

# --- B 无 tag:短 sha;不存在 → create(带 --target)再 upload ---
st 0 "$(SIZE=1300000000 GH_VIEW_RC=1 rc_of run_leg qcow2)" "qcow2 正常路径 → 退出码 0"
st 1 "$(grep -c "^release create $SHORT --target $SHA " "$TMP/gh.log")" "Release 不存在 → create 且 --target 钉本 run commit"
st "qcow2-$SHORT.qcow2" "$(upload_name)" "上传的文件名 = 目标资产名(不是 disk.qcow2)"
st 1 "$(grep -c -- '--clobber --repo Daedalusys/daedalus-core' "$TMP/gh.log")" "upload 带 --clobber(同一 ref 重跑幂等)"
st 0 "$(find "$TMP/runner-temp" -type f | wc -l | tr -d ' ')" "上传后清理临时落位"
st 1 "$(find "$TMP/ws/output/qcow2" -name '*.qcow2' | wc -l | tr -d ' ')" "产物目录未被污染(仍是 1 个原件)"
# 校验和必须随资产同源产出:内容真能校验住被上传的那份字节,而不是一张空表
st 1 "$(grep -c '^release upload .*SHA256SUMS' "$TMP/gh.log")" "upload 同时带 SHA256SUMS"
st 1 "$( ( cd "$TMP/uploads" && sha256sum -c --strict SHA256SUMS >/dev/null 2>&1 ) && echo 1 || echo 0 )" \
    "SHA256SUMS 能校验被上传的资产字节(内容真的对得上)"

# --- C 已存在且 tag 指向本 run commit → 只补不建 ---
st 0 "$(SIZE=1300000000 GH_VIEW_RC=0 GH_API_SHA="$SHA" rc_of run_leg qcow2)" "已存在且 tag 指向本 commit → 退出码 0"
st 0 "$(grep -c '^release create ' "$TMP/gh.log")" "已存在 → 不再 create"
st 1 "$(grep -c '^release upload ' "$TMP/gh.log")" "已存在 → 仍补传资产"

# --- D 已存在但 tag 指向别的 commit → 必须拒(不覆盖别人的 Release) ---
st 1 "$(SIZE=1300000000 GH_VIEW_RC=0 GH_API_SHA=deadbeefdeadbeefdeadbeefdeadbeefdeadbeef \
    rc_of run_leg qcow2)" "已存在但 tag 指向别的 commit → 退出码 1"
st 0 "$(grep -c '^release upload ' "$TMP/gh.log")" "tag 不匹配 → gh 零上传"

# --- E 并发:create 撞车但另一条腿已建好 → 继续补资产 ---
st 0 "$(SIZE=1300000000 GH_VIEW_RC=1 GH_VIEW_RC2=0 GH_CREATE_RC=1 GH_API_SHA="$SHA" \
    rc_of run_leg qcow2)" "create 撞车但 Release 已存在 → 退出码 0(视为成功)"
st 1 "$(grep -c '^release upload ' "$TMP/gh.log")" "create 撞车后仍补传资产"

# --- F tag push → 用 tag 名 ---
st 0 "$(SIZE=1300000000 GH_VIEW_RC=1 rc_of run_leg qcow2 v0.4.0)" "tag push → 退出码 0"
st "qcow2-v0.4.0.qcow2" "$(upload_name)" "tag push → 上传文件名带 tag"
# 另一条腿已上传过一行时,SHA256SUMS 必须**追加**而不是覆盖 ——
# 一份文件描述本次发布的全部资产。
st 0 "$(SIZE=1300000000 GH_VIEW_RC=1 GH_PREEXISTING_SUMS="deadbeef  iso-verify-v0.4.0.iso" \
    rc_of run_leg qcow2 v0.4.0)" "已有 SHA256SUMS 时 → 退出码 0"
st 2 "$(wc -l <"$TMP/uploads/SHA256SUMS" | tr -d ' ')" "已有一行时 SHA256SUMS 追加为两行(不覆盖别人的)"
st 1 "$(grep -c 'iso-verify-v0.4.0.iso' "$TMP/uploads/SHA256SUMS")" "原有那一行仍在"

# --- G ISO 腿同型 ---
st 0 "$(SIZE=1200000000 GH_VIEW_RC=1 rc_of run_leg iso v0.4.0)" "ISO 正常路径 → 退出码 0"
st "iso-verify-v0.4.0.iso" "$(upload_name)" "ISO 上传文件名 = 目标资产名"

# --- G2 ISO 判定不是 INSTALLED_LOGIN → 不晋级(证据留在 artifact) ---
st 0 "$(SIZE=1200000000 GH_VIEW_RC=1 ISO_VERDICT=BLOCKED_EXTERNAL rc_of run_leg iso v0.4.0)" \
    "ISO 判定 BLOCKED_EXTERNAL → 退出码 0(外因不判红)"
st 0 "$(gh_calls)" "ISO 判定 BLOCKED_EXTERNAL → gh 零调用(不把没验过装机的 ISO 发出去)"
st 0 "$(SIZE=1200000000 GH_VIEW_RC=1 ISO_VERDICT=LIVE_OK rc_of run_leg iso v0.4.0)" \
    "ISO 判定 LIVE_OK(没装机) → 退出码 0"
st 0 "$(gh_calls)" "ISO 判定 LIVE_OK → gh 零调用"
st 1 "$(SIZE=1200000000 GH_VIEW_RC=1 ISO_VERDICT=__MISSING__ rc_of run_leg iso v0.4.0)" \
    "ISO 判定文件缺失 → 退出码 1(不得在无判定时晋级)"

# --- H 超 2GiB → rc2,gh 一次都不调(不产生半截 Release) ---
st 2 "$(SIZE=2200000000 GH_VIEW_RC=1 rc_of run_leg qcow2)" "qcow2 超 2GiB → 退出码 2"
st 0 "$(gh_calls)" "qcow2 超 2GiB → gh 零调用"

# --- I 版本漂开(GITHUB_SHA ≠ 检出 commit)→ 守卫拦 ---
st 1 "$(SIZE=1300000000 GH_VIEW_RC=1 OVERRIDE_SHA=0000000000000000000000000000000000000000 \
    rc_of run_leg qcow2)" "短 sha 与检出 commit 不一致 → 退出码 1"

if [ "$FAIL_N" -eq 0 ]; then
    printf 'RESULT: PROMOTE-TEST PASS\n'
    exit 0
fi
printf 'RESULT: PROMOTE-TEST FAIL (%d)\n' "$FAIL_N"
exit 1
