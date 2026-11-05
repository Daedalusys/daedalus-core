#!/usr/bin/env bash
# 跨仓 workflow 凭据/缓存不变式守门。集中放 core 仓:只有 core 的 test job 同时
# 检出四个兄弟仓(同 policy.toml drift 守门的选址理由),故这里是对**四仓** workflow
# 文件做静态断言的唯一执行点。
#
# 断言的不变式(每一条都对应一种"默认值漂移回隐式"的回归):
#   规则1 每个 actions/checkout 步骤必须有显式 persist-credentials:
#   规则2 每个 actions/setup-go 步骤必须有显式 cache:,取值 ∈ {true,false}
#   规则3 文件 on: 段含 pull_request: 时,该文件的 cache: 一律不得为 true
#        (cache 按分支作用域共享,基础分支能读到 PR 分支写入的条目)
#   规则4 全仓不得出现 pull_request_target
#   规则5 所有 uses: 的 ref 必须是 40 位十六进制 SHA(拒 @v3 之类浮动标签)
#
# 判定必须是 **mapping 解析**,不是全文 grep:注释/文档里出现的 `actions/checkout`
# 字样不是步骤。`--selftest` 用合成样本把这条负例钉住。
#
# 用法:
#   workflow-hygiene.sh [四仓平级根]      # 默认 = 本脚本上溯三级
#   workflow-hygiene.sh --selftest
set -euo pipefail

REPOS="daedalus-core daedalus-sdk daedalus-plugins daedalus-agent"

# scan_root <四仓平级根> —— 逐仓逐 workflow 断言;有违规打印定位并返回 1。
#
# 解析方式:按 YAML 的 mapping 结构走 —— 「步骤」= 以 `- <步骤键>:` 开头的列表项,
# 步骤内只认 `uses:` / `persist-credentials:` / `cache:` 三个键;注释行(去掉 `#`
# 之后)不参与判定。这是 RF4 的要求:注释里提到 `actions/checkout` 不算步骤。
scan_root() {
    local root="$1" repo wf fail=0
    for repo in $REPOS; do
        for wf in "$root/$repo/.github/workflows/"*.yml "$root/$repo/.github/workflows/"*.yaml; do
            [ -f "$wf" ] || continue
            # 单文件解析:awk 程序从 stdin 读,输入文件是 $wf。
            # 违规 → stdout(逐条定位);统计 → stderr。
            awk -f - "$wf" <<'AWK' || fail=1
function report(msg) {
    printf "%s:%d %s\n", FILENAME, cur_line, msg
    violations++
}

function flush_step() {
    if (!in_step) return
    if (cur_uses == "") return
    if (cur_uses ~ /^actions\/checkout@/) {
        n_checkout++
        if (!has_pc)
            report("规则1 checkout 步骤缺显式 persist-credentials")
        else if (cur_pc != "true" && cur_pc != "false")
            report("规则1 persist-credentials 取值非 true/false: " cur_pc)
    }
    if (cur_uses ~ /^actions\/setup-go@/) {
        n_setupgo++
        if (!has_cache)
            report("规则2 setup-go 步骤缺显式 cache")
        else if (cur_cache != "true" && cur_cache != "false")
            report("规则2 cache 取值非 true/false: " cur_cache)
        else if (has_pr && cur_cache == "true")
            report("规则3 pull_request 触发的文件里 cache: true")
    }
    # 规则5:所有 uses: 的 ref 必须是 40 位十六进制 SHA(拒浮动标签)。
    at = index(cur_uses, "@")
    if (at == 0) {
        report("规则5 uses 未钉 SHA(无 @ref): " cur_uses)
    } else {
        ref = substr(cur_uses, at + 1)
        sub(/[[:space:]].*$/, "", ref)
        if (length(ref) != 40 || ref ~ /[^0-9a-f]/)
            report("规则5 uses ref 非 40 位 SHA: " ref)
    }
}

BEGIN { in_step = 0; has_pr = 0; violations = 0; n_checkout = 0; n_setupgo = 0 }

{
    line = $0
    code = line
    sub(/#.*$/, "", code)

    # 规则3 的文件级前提:on: 段含 pull_request: 键。
    # `pull_request_target:` 不会被它误判(冒号位置不同)。
    if (code ~ /^[[:space:]]*pull_request:/) has_pr = 1
    # 规则4:pull_request_target 一律禁止。
    if (code ~ /pull_request_target/)
        report("规则4 出现 pull_request_target")

    # 离开上一个 job(顶层键或 job 名)→ 结束当前步骤上下文。
    if (line ~ /^[A-Za-z0-9_-]+:/ || line ~ /^  [A-Za-z0-9_-]+:/) {
        in_step = 0
    }

    # 步骤起始:列表项的首键属于步骤键集合。
    # `on:` 段的 `- "**/*.md"` 之类不匹配 —— 它们不是步骤。
    if (line ~ /^[[:space:]]*-[[:space:]]*(name|uses|id|shell|run|with|env|if|working-directory|continue-on-error|timeout-minutes)[[:space:]]*:/) {
        flush_step()
        in_step = 1
        cur_line = NR
        cur_uses = ""; cur_pc = ""; has_pc = 0; pc_line = 0
        cur_cache = ""; has_cache = 0; cache_line = 0
        # 步骤首键就是 uses: 的形态:`- uses: owner/action@ref`
        inline = code
        sub(/^[[:space:]]*-[[:space:]]*/, "", inline)
        if (inline ~ /^uses:[[:space:]]*/) {
            v = inline
            sub(/^uses:[[:space:]]*/, "", v)
            sub(/[[:space:]]+$/, "", v)
            cur_uses = v
        }
        next
    }
    if (!in_step) next

    if (code ~ /^[[:space:]]*uses:[[:space:]]*/) {
        v = code
        sub(/^[[:space:]]*uses:[[:space:]]*/, "", v)
        sub(/[[:space:]]+$/, "", v)
        if (cur_uses == "") cur_uses = v
    }
    if (code ~ /^[[:space:]]*persist-credentials:[[:space:]]*/) {
        v = code
        sub(/^[[:space:]]*persist-credentials:[[:space:]]*/, "", v)
        sub(/[[:space:]]+$/, "", v)
        cur_pc = v; has_pc = 1; pc_line = NR
    }
    if (code ~ /^[[:space:]]*cache:[[:space:]]*/) {
        v = code
        sub(/^[[:space:]]*cache:[[:space:]]*/, "", v)
        sub(/[[:space:]]+$/, "", v)
        cur_cache = v; has_cache = 1; cache_line = NR
    }
}

END {
    flush_step()
    printf "[scan] %s: checkout %d 处 / setup-go %d 处\n", FILENAME, n_checkout, n_setupgo > "/dev/stderr"
    if (violations > 0) exit 1
}
AWK
        done
    done
    return "$fail"
}

# ---------------------------------------------------------------- 自检 ---------
if [ "${1:-}" = "--selftest" ]; then
    fail_n=0
    st() { # st <期望> <实际> <说明>
        if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
        else printf '  FAIL  %s (期望 %q,实际 %q)\n' "$3" "$1" "$2"; fail_n=$((fail_n + 1)); fi
    }
    printf '== workflow-hygiene.sh --selftest ==\n'

    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    wf="$tmp/daedalus-core/.github/workflows"
    mkdir -p "$wf"

    # probe <样本名> <yaml 正文> —— 造一个单文件仓骨架,返回 scan_root 的输出。
    probe() {
        local name="$1" body="$2"
        rm -f "$wf"/*.yml
        printf '%s\n' "$body" >"$wf/$name.yml"
        scan_root "$tmp" 2>&1 || true
    }

    HDR='name: t
on:
  push:
jobs:
  j:
    runs-on: ubuntu-24.04
    steps:'
    SHA='3d3c42e5aac5ba805825da76410c181273ba90b1'
    SETUP_SHA='b7ad1dad31e06c5925ef5d2fc7ad053ef454303e'

    out="$(probe checkout-no-with "$HDR
      - uses: actions/checkout@$SHA")"
    st 1 "$(printf '%s' "$out" | grep -c '规则1')" "规则1:checkout 无 with: → 违规"

    out="$(probe comment-mention "$HDR
      # 这里提到 actions/checkout 与 setup-go 只是说明
      - uses: actions/checkout@$SHA
        with:
          persist-credentials: false
      - uses: actions/setup-go@$SETUP_SHA
        with:
          go-version: 1.26.x
          cache: false")"
    # RF4 负例:注释里的字样不得被当成步骤,故本样本除"无 setup-go 缺失"外零违规。
    st 0 "$(printf '%s' "$out" | grep -c '规则')" "RF4 负例:注释里的字样不算步骤(全绿)"

    out="$(probe pr-with-cache-true "name: t
on:
  pull_request:
  push:
jobs:
  j:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/setup-go@$SETUP_SHA
        with:
          go-version: 1.26.x
          cache: true")"
    st 1 "$(printf '%s' "$out" | grep -c '规则3')" "规则3:pull_request 触发的文件里 cache: true → 违规"

    out="$(probe release-with-cache-true "$HDR
      - uses: actions/setup-go@$SETUP_SHA
        with:
          go-version: 1.26.x
          cache: true")"
    st 0 "$(printf '%s' "$out" | grep -c '规则')" "规则3 反例:无 pull_request 的发布腿 cache: true → 不违规"

    out="$(probe floating-tag "$HDR
      - uses: actions/setup-go@v7.0.0
        with:
          go-version: 1.26.x
          cache: false")"
    st 1 "$(printf '%s' "$out" | grep -c '规则5')" "规则5:浮动标签 @v7.0.0 → 违规"

    out="$(probe prt "name: t
on:
  pull_request_target:
jobs:
  j:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@$SHA
        with:
          persist-credentials: false")"
    st 1 "$(printf '%s' "$out" | grep -c '规则4')" "规则4:pull_request_target → 违规"

    out="$(probe cache-bad-value "$HDR
      - uses: actions/setup-go@$SETUP_SHA
        with:
          go-version: 1.26.x
          cache: yes")"
    st 1 "$(printf '%s' "$out" | grep -c '规则2')" "规则2:cache 取值非 true/false → 违规"

    if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
    echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
fi

# ---------------------------------------------------------------- 正式流程 -----
ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"
if scan_root "$ROOT"; then
    echo "OK: 四仓 workflow 的 persist-credentials / cache / 钉 SHA 全部显式"
else
    echo "ERROR: workflow 凭据/缓存不变式被破坏(见上方逐条定位)" >&2
    exit 1
fi
