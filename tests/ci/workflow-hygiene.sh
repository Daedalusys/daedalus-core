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

# 覆盖度下限:站点总数低于它即认为 workflow 被删/改名,守门不得静默变绿。
# 2026-11-05 实测基线:checkout 17 处 / setup-go 8 处(四仓)。
COVERAGE_MIN_CHECKOUT=15
COVERAGE_MIN_SETUPGO=7

# check_coverage <四仓平级根> [<站点计数文件>] —— 守门的覆盖度自证。
#   规则6 四个仓的 workflow 目录必须都在(缺一个 = 没看那份文件,不能说"全部显式")
#   规则7 站点总数不得低于基线下限
# 为什么需要它:scan_root 遇到不存在的目录只会静默跳过 —— 一个"扫了 0 个文件"
# 的守门若还打印 OK,就与它要消灭的 `|| true` 是同一种假绿。
check_coverage() {
    local root="$1" counts="${2:-}" repo d tc=0 ts=0 missing=0 c s
    for repo in $REPOS; do
        d="$root/$repo/.github/workflows"
        if [ ! -d "$d" ]; then
            echo "规则6 $repo: 缺 $d —— 守门未检视该仓 workflow,不得报 OK" >&2
            missing=1
        fi
    done
    [ "$missing" -eq 0 ] || return 1
    if [ -n "$counts" ] && [ -s "$counts" ]; then
        read -r tc ts < <(awk '{a+=$1; b+=$2} END {print a+0, b+0}' "$counts")
    fi
    if [ "$tc" -lt "$COVERAGE_MIN_CHECKOUT" ] || [ "$ts" -lt "$COVERAGE_MIN_SETUPGO" ]; then
        echo "规则7 覆盖不足:checkout $tc 处 / setup-go $ts 处(下限 $COVERAGE_MIN_CHECKOUT/$COVERAGE_MIN_SETUPGO)—— workflow 被删或改名?" >&2
        return 1
    fi
    printf 'checkout %d 处 / setup-go %d 处' "$tc" "$ts"
}

# scan_root <四仓平级根> [<站点计数文件>] —— 逐仓逐 workflow 断言;有违规打印定位并返回 1。
#
# 解析方式:按 YAML 的 mapping 结构走 —— 「步骤」= 以 `- <步骤键>:` 开头的列表项,
# 步骤内只认 `uses:` / `persist-credentials:` / `cache:` 三个键;注释行(去掉 `#`
# 之后)不参与判定。这是 RF4 的要求:注释里提到 `actions/checkout` 不算步骤。
scan_root() {
    local root="$1" counts="${2:-}" repo wf fail=0
    for repo in $REPOS; do
        for wf in "$root/$repo/.github/workflows/"*.yml "$root/$repo/.github/workflows/"*.yaml; do
            [ -f "$wf" ] || continue
            # 单文件解析:awk 程序从 stdin 读,输入文件是 $wf。
            # 违规 → stdout(逐条定位);统计 → stderr;站点计数 → $counts。
            awk -v counts="$counts" -f - "$wf" <<'AWK' || fail=1
function report_at(l, msg) {
    printf "%s:%d %s\n", FILENAME, l, msg
    violations++
}

function strip_quotes(s,   n, q1, q2, q) {
    n = length(s)
    if (n < 2) return s
    q = sprintf("%c", 39)
    q1 = substr(s, 1, 1); q2 = substr(s, n, 1)
    if ((q1 == "\"" && q2 == "\"") || (q1 == q && q2 == q)) return substr(s, 2, n - 2)
    return s
}

# key_of —— 该行是不是一个 plain mapping 键(`key:` 起头)?返回键名,否则空串。
function key_of(line,   k) {
    if (line !~ /^[ \t]*[A-Za-z0-9_".-]+:/) return ""
    k = line
    sub(/^[ \t]*/, "", k)
    sub(/:.*$/, "", k)
    return k
}

# val —— 取 `key: <value>` 的 value(去掉注释与首尾空白)。
function val(line,   v) {
    v = line
    sub(/^[ \t]*/, "", v)
    sub(/^[^:]*:[ \t]*/, "", v)
    sub(/[ \t]+$/, "", v)
    return v
}

# check_ref —— 规则5:`uses:` 的 ref 必须是 40 位十六进制 SHA。
# `docker://` 按 digest/tag 钉,本地 action(`./`)没有 ref 可钉 —— 两者不适用本规则。
function check_ref(v, l,   at, ref) {
    if (v ~ /^docker:\/\// || v ~ /^\.\.?\//) return
    at = index(v, "@")
    if (at == 0) { report_at(l, "规则5 uses 未钉 SHA(无 @ref): " v); return }
    ref = substr(v, at + 1)
    sub(/[ \t].*$/, "", ref)
    if (length(ref) != 40 || ref ~ /[^0-9a-f]/)
        report_at(l, "规则5 uses ref 非 40 位 SHA: " ref)
}

function flush_step() {
    if (!in_step || cur_uses == "") { in_step = 0; return }
    if (cur_uses ~ /^actions\/checkout@/) {
        n_checkout++
        if (!has_pc)
            report_at(cur_line, "规则1 checkout 步骤缺显式 persist-credentials(with: 块内)")
        else if (cur_pc != "true" && cur_pc != "false")
            report_at(pc_line, "规则1 persist-credentials 取值非 true/false: " cur_pc)
    }
    if (cur_uses ~ /^actions\/setup-go@/) {
        n_setupgo++
        if (!has_cache)
            report_at(cur_line, "规则2 setup-go 步骤缺显式 cache")
        else if (cur_cache != "true" && cur_cache != "false")
            report_at(cache_line, "规则2 cache 取值非 true/false: " cur_cache)
    }
    # 规则3 是**文件级**的:任何 action 的 `cache: true` 都要在 END 里过一遍 has_pr,
    # 因为 `on:` 可能写在文件任意位置(YAML 映射序自由)。
    if (has_cache && cur_cache == "true") cache_true_line[++n_cache_true] = cache_line
    check_ref(cur_uses, uses_line)
    in_step = 0
}

BEGIN {
    in_step = 0; in_with = 0; with_indent = -1; block_indent = -1
    has_pr = 0; violations = 0; n_checkout = 0; n_setupgo = 0; n_cache_true = 0
    cur_uses = ""; uses_line = 0; cur_line = 0
    has_pc = 0; cur_pc = ""; pc_line = 0
    has_cache = 0; cur_cache = ""; cache_line = 0
    step_indent = -1
}

{
    code = $0
    sub(/#.*$/, "", code)

    ind = 0
    tmp = code
    while (substr(tmp, 1, 1) == " ") { ind++; tmp = substr(tmp, 2) }

    # 块标量(`run: |` / `>`)的内容不是 YAML 结构 —— 整段跳过,
    # 否则脚本里生成的 workflow 文本会被误判成步骤(RF4 同类假阳性)。
    if (block_indent >= 0) {
        if (code ~ /^[ \t]*$/) next
        if (ind >= block_indent) next
        block_indent = -1
    }
    if (code ~ /^[ \t]*$/) next

    # 规则3 的文件级前提:on: 段含 pull_request。三种合法写法都要认 ——
    # 块式键 / 流式(`on: [push, pull_request]`)/ 标量 / 块式列表项。
    if (code ~ /^[ \t]*pull_request:/)            has_pr = 1
    if (code ~ /^[ \t]*on:[ \t]*.*pull_request/)  has_pr = 1
    if (code ~ /^[ \t]*-[ \t]*pull_request[ \t]*$/) has_pr = 1

    # 规则4:pull_request_target 一律禁止(报告在它自己那一行)。
    if (code ~ /pull_request_target/) report_at(NR, "规则4 出现 pull_request_target")

    # 块标量起始:key: | 或 key: >(带可选 chomping 指示符)。
    if (code ~ /^[ \t]*[A-Za-z0-9_".-]+:[ \t]*[|>][-+0-9]*[ \t]*$/) {
        block_indent = ind + 1
        next
    }

    # 步骤起始:`- <步骤键>:` 开头的列表项。
    if (code ~ /^[ \t]*-[ \t]*(name|uses|id|shell|run|with|env|if|working-directory|continue-on-error|timeout-minutes)[ \t]*:/) {
        flush_step()
        in_step = 1
        step_indent = ind
        cur_line = NR; uses_line = NR
        cur_uses = ""
        has_pc = 0; cur_pc = ""; pc_line = 0
        has_cache = 0; cur_cache = ""; cache_line = 0
        in_with = 0; with_indent = -1
        inline = code
        sub(/^[ \t]*-[ \t]*/, "", inline)
        if (inline ~ /^uses:[ \t]*/) {
            cur_uses = strip_quotes(val(inline))
        } else if (inline ~ /^with:[ \t]*$/) {
            in_with = 1; with_indent = ind + 2
        }
        next
    }

    k = key_of(code)

    # job / 顶层边界:结束步骤上下文(用相对缩进判断,不假定 2 空格风格)。
    # job 级 reusable workflow 的 `uses:` 同样受规则5 约束。
    if (k != "" && (in_step == 0 || ind <= step_indent)) {
        flush_step()
        in_step = 0
        if (k == "uses") check_ref(strip_quotes(val(code)), NR)
        next
    }

    if (!in_step) next

    # `with:` 作用域:只有它下面的键才算「显式声明」—— 放在 step 级 env: 里不算。
    if (k == "with" && ind == step_indent + 2) { in_with = 1; with_indent = ind; next }
    if (in_with && ind <= with_indent) in_with = 0

    if (in_with) {
        if (k == "persist-credentials") { cur_pc = val(code); has_pc = 1; pc_line = NR }
        else if (k == "cache")         { cur_cache = val(code); has_cache = 1; cache_line = NR }
        next
    }

    if (k == "uses" && cur_uses == "") cur_uses = strip_quotes(val(code))
}

END {
    flush_step()
    if (has_pr)
        for (i = 1; i <= n_cache_true; i++)
            report_at(cache_true_line[i], "规则3 pull_request 触发的文件里 cache: true")
    printf "[scan] %s: checkout %d 处 / setup-go %d 处\n", FILENAME, n_checkout, n_setupgo > "/dev/stderr"
    if (counts != "") printf "%d %d\n", n_checkout, n_setupgo >> counts
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

    # 8) on: 的流式写法(`on: [push, pull_request]`)也必须被认成 PR 可达 ——
    #    只认「`pull_request:` 独占一行」的解析会把这种文件漏成「非 PR 可达」。
    out="$(probe on-flow-list "name: t
on: [push, pull_request]
jobs:
  j:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/setup-go@$SETUP_SHA
        with:
          go-version: 1.26.x
          cache: true")"
    st 1 "$(printf '%s' "$out" | grep -c '规则3')" "规则3:`on: [.., pull_request]` 流式写法 → 违规"

    # 9) `on:` 写在 `jobs:` 之后(YAML 映射序自由)同样必须被认出来。
    out="$(probe on-after-jobs "name: t
jobs:
  j:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/setup-go@$SETUP_SHA
        with:
          go-version: 1.26.x
          cache: true
on:
  pull_request:")"
    st 1 "$(printf '%s' "$out" | grep -c '规则3')" "规则3:\`on:\` 写在 jobs 之后 → 违规"

    # 10) job 级 reusable workflow 的 uses: 也要受规则5 约束(它同样要钉 SHA)。
    out="$(probe job-level-uses "name: t
on:
  push:
jobs:
  j:
    uses: org/repo/.github/workflows/other.yml@v1")"
    st 1 "$(printf '%s' "$out" | grep -c '规则5')" "规则5:job 级 reusable workflow 浮动 tag → 违规"

    # 11) 显式键必须在 `with:` 块内 —— 放在 step 级 `env:` 里不算数。
    out="$(probe pc-outside-with "$HDR
      - uses: actions/checkout@$SHA
        env:
          persist-credentials: false")"
    st 1 "$(printf '%s' "$out" | grep -c '规则1')" "规则1:persist-credentials 不在 with: 内 → 违规"

    # 12) `run: |` 块里的内容不是步骤 —— 脚本里生成 workflow 不该被判违规。
    out="$(probe run-block-not-a-step "$HDR
      - name: gen
        run: |
          cat > wf.yml <<EOF
          - uses: actions/checkout@$SHA
          EOF")"
    st 0 "$(printf '%s' "$out" | grep -c '规则')" "块标量里的 \`- uses:\` 不是步骤(不误报)"

    # 13) `uses:` 的值可以带引号 —— 带引号时仍要认出 checkout,且不得误报规则5。
    out="$(probe quoted-uses "$HDR
      - uses: \"actions/checkout@$SHA\"")"
    st 1 "$(printf '%s' "$out" | grep -c '规则1')" "带引号的 checkout → 规则1 仍触发"
    st 0 "$(printf '%s' "$out" | grep -c '规则5')" "带引号的 checkout → 规则5 不误报"

    # 14) `docker://` 与本地 action 没有可钉的 ref —— 不得报规则5。
    out="$(probe docker-and-local "$HDR
      - uses: docker://alpine:3.20
      - uses: ./.github/actions/local-thing")"
    st 0 "$(printf '%s' "$out" | grep -c '规则')" "docker:// 与本地 action → 不报规则5"

    # 15) 规则3 是**文件级**的:不限于 setup-go,任何 action 的 `cache: true` 都算。
    out="$(probe other-action-cache-true "name: t
on:
  pull_request:
jobs:
  j:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/setup-node@$SHA
        with:
          node-version: 20
          cache: true")"
    st 1 "$(printf '%s' "$out" | grep -c '规则3')" "规则3:非 setup-go 的 action 也受约束 → 违规"

    # 16) 覆盖度下限:四仓目录都在、但一个站点都没有 → 不得报 OK
    #     (否则「workflow 被删/改名」会静默变绿 —— 与 `|| true` 同一种假绿)。
    rm -f "$wf"/*.yml
    for r in daedalus-sdk daedalus-plugins daedalus-agent; do mkdir -p "$tmp/$r/.github/workflows"; done
    empty_counts="$(mktemp)"; : > "$empty_counts"
    rc=0; check_coverage "$tmp" "$empty_counts" >/dev/null 2>&1 || rc=$?
    st 1 "$rc" "覆盖度:站点数为 0 → 规则7 拒(不得静默变绿)"
    rm -f "$empty_counts"

    # 17) 缺一个仓的 workflow 目录 → 规则6 拒(守门没看那份文件就不能说"全部显式")。
    rm -rf "$tmp/daedalus-agent/.github/workflows"
    rc=0; check_coverage "$tmp" >/dev/null 2>&1 || rc=$?
    st 1 "$rc" "覆盖度:缺 daedalus-agent 的 workflow 目录 → 规则6 拒"

    if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
    echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
fi

# ---------------------------------------------------------------- 正式流程 -----
ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"
COUNTS="$(mktemp)"
trap 'rm -f "$COUNTS"' EXIT
ok=1
scan_root "$ROOT" "$COUNTS" || ok=0
cov="$(check_coverage "$ROOT" "$COUNTS")" || ok=0
if [ "$ok" -eq 1 ]; then
    echo "OK: 四仓 workflow 的 persist-credentials / cache / 钉 SHA 全部显式(仓 4 个 / $cov)"
else
    echo "ERROR: workflow 凭据/缓存不变式被破坏或守门覆盖不全(见上方逐条定位)" >&2
    exit 1
fi
