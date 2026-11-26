#!/usr/bin/env bash
# 树内容器凭据门禁:工作树里不允许出现模型网关的 `sk-gw-` key;靶场证据里的 AI 客户端
# 配置(opencode.json)只允许留脱敏后的值。
#
# 为什么要有这条:2026-11-24 的靶场证据把 10 份 opencode.json 连同 provider 的 apiKey
# 与每轮 Bearer 令牌提交进了**公开**仓库。当前树已脱敏,但「脱敏靠人记得」这件事本身
# 不可靠 —— 这条门禁把「再来一次」挡在 CI。
#
# 只看已跟踪文件:
#   - 不扫历史。历史里的原文要靠轮换 key + 是否重写来解决,不是这条门禁的职责;
#   - 不扫 .gitignore 掉的东西。本地备忘(轮换清单里会写 key 前缀)不该被扫。
#
# 用法: secret-scan.sh [--selftest]
set -euo pipefail

ROOT="${SECRET_SCAN_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
EVIDENCE_DIR='tests/real-machine/logs'

# 拆成两段拼:脚本自己会含这些字样,不能自己命中自己。
KEY_RE="sk""-gw-[A-Za-z0-9]{8,}"
# 脱敏后的合法取值:空串(客户端配置里本来就可能为空)与 REDACTED。
ALLOWED_VALUE_RE='^$|^REDACTED$'

fail_n=0
fail() { printf 'FAIL: %s\n' "$1" >&2; fail_n=$((fail_n + 1)); }
ok() { printf '  OK  %s\n' "$1"; }

# secret_values <文本> —— 从一段客户端配置文本里抽出**没有脱敏**的凭据值。
# 只认两种载体:`"apiKey": "…"` 与 `"Authorization": "Bearer …"`。
# 空值与 REDACTED 是脱敏后的合法形态;其余(包括 `from-env` 这种占位)一律算泄漏 ——
# 证据目录里不需要出现任何非字面量的取值。
secret_values() {
    grep -oE '"(apiKey|Authorization)"[[:space:]]*:[[:space:]]*"[^"]*"' <<<"$1" 2>/dev/null \
        | sed -E 's/^"(apiKey|Authorization)"[[:space:]]*:[[:space:]]*"(.*)"$/\2/' \
        | sed -E 's/^Bearer //' \
        | grep -vE "$ALLOWED_VALUE_RE" || true
}

# client_configs <路径列表文本> —— 从 `git ls-files` 的输出里挑出证据目录下的客户端配置。
# 客户端配置是模型这边**唯一的**凭据落盘点:它会抄 provider 的 apiKey 与每轮 Bearer。
client_configs() {
    printf '%s\n' "$1" | grep -E "(^|/)$EVIDENCE_DIR/.*opencode\.json$" || true
}

scan() {
    local root=$1 files hit path leaked

    files=$(git -C "$root" ls-files)

    # 1) 整个树里不允许出现模型网关 key。
    hit=0
    while IFS= read -r path; do
        [ -n "$path" ] || continue
        fail "$path 含模型网关 key"
        hit=$((hit + 1))
    done < <(git -C "$root" grep -lE "$KEY_RE" -- . 2>/dev/null || true)
    [ "$hit" -eq 0 ] && ok "工作树里没有 sk-gw- key"

    # 2) 证据目录里的客户端配置只允许留脱敏值。
    hit=0
    while IFS= read -r path; do
        [ -n "$path" ] || continue
        leaked=$(secret_values "$(git -C "$root" show ":$path" 2>/dev/null)" || true)
        if [ -n "$leaked" ]; then
            fail "$path 里有未脱敏的凭据值($(printf '%s' "$leaked" | head -1 | cut -c1-24)…)"
            hit=$((hit + 1))
        fi
    done < <(client_configs "$files")
    [ "$hit" -eq 0 ] && ok "证据里的客户端配置都是脱敏值"

    [ "$fail_n" -eq 0 ]
}

if [ "${1:-}" = "--selftest" ]; then
    # 端到端自检:在临时 git 仓里造真实形状的树,跑一遍本脚本看退出码。
    # 只测函数不够 —— 门禁的失效方式恰恰是「判定函数对、接线错」(扫错目录、
    # 只扫工作树不扫索引、命中后不返回非零)。三条断言各对应一类退化。
    printf '== secret-scan.sh --selftest ==\n'
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT

    st() { # st <期望> <实际> <说明>
        if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
        else printf '  FAIL  %s(期望 %q,实际 %q)\n' "$3" "$1" "$2"; fail_n=$((fail_n + 1)); fi
    }

    # mkfix <名字> —— 造一个只有证据目录骨架的临时 git 仓,返回路径。
    mkfix() {
        local d="$tmp/$1"
        mkdir -p "$d/$EVIDENCE_DIR/2026-01-01-probe/rounds/r1"
        git -C "$d" init -q
        printf '%s' "$d"
    }
    # seed <仓> <相对路径> <内容> —— 写文件并进索引(门禁只看已跟踪文件)。
    seed() {
        mkdir -p "$(dirname "$1/$2")"
        printf '%s\n' "$3" >"$1/$2"
        git -C "$1" add -A
    }
    # run <仓> —— 跑门禁本身,输出放到 $out,退出码放到 $rc。
    run() {
        out=$(SECRET_SCAN_ROOT="$1" bash "$0" 2>&1) && rc=0 || rc=$?
    }

    clean=$(mkfix clean)
    seed "$clean" "$EVIDENCE_DIR/2026-01-01-probe/rounds/r1/opencode.json" \
        '{ "provider": { "aigw-main": { "options": { "apiKey": "REDACTED" } } },
           "mcp": { "p": { "headers": { "Authorization": "Bearer REDACTED" } } } }'
    seed "$clean" "docs/note.md" '笔记里提到 sk-gw- 前缀,但不是 key。'
    seed "$clean" "cmd/x.json" '{ "apiKey": "from-env" }'
    run "$clean"
    st 0 "$rc" "干净树(证据已脱敏 + 非证据占位值)通过"

    dirty_key=$(mkfix dirty-key)
    # 拆两段拼,本脚本自身不能命中自己的用例。
    seed "$dirty_key" "leak.txt" '{ "k": "'"sk""-gw-abcdef123456"'" }'
    run "$dirty_key"
    st 1 "$rc" "明文 key 在任意位置 → 失败"
    st 1 "$(printf '%s' "$out" | grep -c 'leak.txt' || true)" "失败信息点名文件"

    dirty_value=$(mkfix dirty-value)
    seed "$dirty_value" "$EVIDENCE_DIR/2026-01-01-probe/rounds/r1/opencode.json" \
        '{ "provider": { "options": { "apiKey": "real-value-here" } } }'
    run "$dirty_value"
    st 1 "$rc" "证据配置里的未脱敏 apiKey → 失败"

    dirty_bearer=$(mkfix dirty-bearer)
    seed "$dirty_bearer" "$EVIDENCE_DIR/2026-01-01-probe/rounds/r1/opencode.json" \
        '{ "mcp": { "p": { "headers": { "Authorization": "Bearer 0123456789abcdef" } } } }'
    run "$dirty_bearer"
    st 1 "$rc" "证据配置里的明文 Bearer → 失败"

    placeholder=$(mkfix placeholder)
    seed "$placeholder" "$EVIDENCE_DIR/2026-01-01-probe/rounds/r1/opencode.json" \
        '{ "provider": { "options": { "apiKey": "from-env" } } }'
    run "$placeholder"
    st 1 "$rc" "证据配置里的占位值 → 失败(证据只留字面量)"

    if [ "$fail_n" -ne 0 ]; then
        printf 'selftest 失败:%d 项\n' "$fail_n" >&2
        exit 1
    fi
    printf 'selftest 全过\n'
    exit 0
fi

printf '== secret-scan.sh(扫描 %s)==\n' "$ROOT"
if scan "$ROOT"; then
    printf 'OK: 工作树无凭据泄漏\n'
    exit 0
fi
printf 'FAIL: %d 处凭据泄漏(见上)\n' "$fail_n" >&2
exit 1
