#!/usr/bin/env bash
# =============================================================================
# probe-blueprint-ui.sh —— A13:daedalus-blueprint-ui 单元(本地 Web UI)真机证据
# =============================================================================
# 证伪什么
#   core#152 把 `daedalus.blueprint` 的第二种形态(常驻本地 Web UI)合进镜像,
#   但仓库侧只有**构建期**门禁(tests/build/blueprint-ui-unit.sh 比对单元文本与
#   drop-in 漂移),没有任何一条真机证据证明:单元真的装进了镜像、真的能起来、
#   四道防护真的挡得住、沙箱真的落在单元上。构建期门禁过 ≠ 真机上它能听 8787。
#
# 断言链
#   A13-1 单元文件在镜像里,ExecStart 是 `daedalus-blueprint ui --listen <回环>:8787`
#         —— 监听 0.0.0.0 等于把「本机运维界面」变成局域网服务,不许;
#   A13-2 插件二进制认得 `ui` 子命令(输出含 -listen),顺带证明镜像里拉到的
#         插件 release ≥ v0.5.0(v0.5.0 起才有 ui);
#   A13-3 systemctl start 后 is-active=active 且 http://127.0.0.1:8787/ = 200;
#   A13-4 四道防护里的两条在**真单元**上成立:Host: evil.example → 403;
#         POST /api/apply 不带 X-Daedalus-Ui → 403;
#   A13-5 沙箱真的落在单元上(DynamicUser / ProtectSystem / NoNewPrivileges /
#         SystemCallFilter 四项)。
#
# 边界
#   * 本探针只起停这一条单元,**不写任何蓝图输出目录**(apply 路径由 A5 覆盖);
#     结尾一定 stop —— guest 是快照,但不留运行中单元;
#   * HTTP 客户端:guest 里有 curl 用 curl,没有则用 python3 urllib;两条路径
#     都必须给出状态码 —— 「403」与「连不上」是两种完全不同的结论,不许混同;
#   * A13-2 的 `--help` 退出码是 2(Go flag 包行为),**不**据此判失败。
#
# 用法: sudo VERIFY_WORK=/tmp/probe-a13 bash tests/real-machine/probe-blueprint-ui.sh
# =============================================================================
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export VERIFY_WORK="${VERIFY_WORK:-/tmp/daedalus-probe-suite}"
export DRIVER_LOG="$VERIFY_WORK/probe-blueprint-ui.log"
# shellcheck source=lib/guest-driver.sh
source "$HERE/lib/guest-driver.sh"
DRIVER_QEMU_SNAPSHOT=1

UNIT="/usr/lib/systemd/system/daedalus-blueprint-ui.service"
UI_BIN="/opt/daedalus/plugins/daedalus.blueprint/bin/daedalus-blueprint"
UI_HOST="127.0.0.1"
UI_PORT="8787"
UI_URL="http://${UI_HOST}:${UI_PORT}"

driver_init
driver_boot

# gcmd —— 在 guest 里跑一条命令;去掉 CR(ssh 传输偶尔带 \r,会让所有比较落空)。
gcmd() { gssh "$1" 2>&1 | tr -d '\r'; }

# ---------------------------------------------------------------- HTTP 客户端
# guest 里 curl 由 65-ai-safety 装上,python3 是基础包;两者取其一即可。
HTTP_CLIENT="$(gcmd 'command -v curl >/dev/null 2>&1 && echo curl || { command -v python3 >/dev/null 2>&1 && echo python3; }')"
info "guest HTTP 客户端: ${HTTP_CLIENT:-<既无 curl 也无 python3>}"

if [ "$HTTP_CLIENT" = "python3" ]; then
    # python3 兜底:必须把「403」与「连不上」分开 —— HTTPError 带状态码,
    # 连接层异常没有;后者如实标 ERR,不许编一个状态码出来。
    cat > "$DRIVER_WORK/probe-a13-http.py" <<'PYEOF'
import sys
import urllib.error
import urllib.request

url = sys.argv[1]
method = (sys.argv[2] if len(sys.argv) > 2 else "GET").upper()
host = sys.argv[3] if len(sys.argv) > 3 else ""

headers = {}
if host:
    # http.client 在请求头里已带 host 时不再自己塞 Host,故这里能覆盖。
    headers["Host"] = host
data = b"" if method == "POST" else None
req = urllib.request.Request(url, data=data, method=method, headers=headers)
try:
    with urllib.request.urlopen(req, timeout=5) as resp:
        print(resp.status)
except urllib.error.HTTPError as exc:
    print(exc.code)
except Exception as exc:
    print("ERR:%s" % exc)
PYEOF
    gssh_scp "$DRIVER_WORK/probe-a13-http.py" /tmp/probe-a13-http.py \
        || die "HTTP 兜底脚本送进 guest 失败"
fi

# http_code <url> [method] [host-header] —— 取 HTTP 状态码;连不上返回空串。
http_code() {
    local url="$1" method="${2:-GET}" hosthdr="${3:-}" out=""
    if [ "$HTTP_CLIENT" = "curl" ]; then
        local hdr=""
        [ -n "$hosthdr" ] && hdr="-H Host:${hosthdr}"
        out="$(gcmd "curl -s -o /dev/null -w '%{http_code}' --max-time 5 -X ${method} ${hdr} '${url}' 2>/dev/null")"
    elif [ "$HTTP_CLIENT" = "python3" ]; then
        out="$(gcmd "python3 /tmp/probe-a13-http.py '${url}' '${method}' '${hosthdr}' 2>/dev/null")"
    fi
    # curl 连不上时 -w 照样打印 000:那是「没有响应」,不是状态码,折成空串让
    # 上层的等待循环继续重试,而不是立刻判成 500 类的失败。
    printf '%s' "$out" | grep -oE '[0-9]{3}' | head -1 | sed 's/^000$//'
}

# ---------------------------------------------------------------------- A13-1
say "A13-1 · 单元文件与 ExecStart(必须监听回环,不许 0.0.0.0)"
UNIT_PRESENT="$(gcmd "test -f $UNIT && echo yes || echo no")"
EXEC_LINE="$(gcmd "sed -n 's/^ExecStart=//p' $UNIT 2>/dev/null" | head -1)"
ENABLED_STATE="$(gcmd "systemctl is-enabled daedalus-blueprint-ui 2>&1")"
info "单元存在: ${UNIT_PRESENT:-?} · is-enabled: ${ENABLED_STATE:-?} · 单元路径: $UNIT"
info "ExecStart: ${EXEC_LINE:-<读不到>}"

LISTEN=""
printf '%s' "$EXEC_LINE" | grep -q 'daedalus-blueprint ui --listen ' \
    && LISTEN="$(printf '%s' "$EXEC_LINE" | sed -n 's/.*--listen \([^ ]*\).*/\1/p')"

A13_1_MISS=""
[ "$UNIT_PRESENT" = "yes" ] || A13_1_MISS="${A13_1_MISS} 单元文件缺失($UNIT)"
case "$LISTEN" in
    "${UI_HOST}:${UI_PORT}"|"localhost:${UI_PORT}") ;;
    *) A13_1_MISS="${A13_1_MISS} ExecStart 的 --listen 不是 ${UI_HOST}:${UI_PORT}(实为 '${LISTEN:-<未解析出>}')" ;;
esac
printf '%s' "$EXEC_LINE" | grep -q '0\.0\.0\.0' && A13_1_MISS="${A13_1_MISS} ExecStart 含 0.0.0.0(会把本机界面变成局域网服务)"
if [ -z "$A13_1_MISS" ]; then
    driver_verdict PASS "A13-1 单元装进镜像且 ExecStart 是 'daedalus-blueprint ui --listen ${LISTEN}'(回环:${UI_PORT})"
else
    driver_verdict FAIL "A13-1 单元/ExecStart 不符合预期:${A13_1_MISS}(ExecStart 原文: ${EXEC_LINE:-<读不到>})"
fi

# ---------------------------------------------------------------------- A13-2
say "A13-2 · 插件二进制认得 ui 子命令(v0.5.0 起才有)"
# timeout 是必须的:旧版插件不认识 ui,会把 "ui" 当成 stdio MCP 模式跑起来并**卡住**,
# 不设上限的话这条探针会挂死而不是给出结论。
HELP_OUT="$(gcmd "timeout 15 $UI_BIN ui --help 2>&1; echo RC=\$?")"
HELP_RC="$(printf '%s' "$HELP_OUT" | sed -n 's/^RC=//p' | tail -1)"
HELP_BODY="$(printf '%s' "$HELP_OUT" | sed '/^RC=/d')"
info "ui --help rc=${HELP_RC:-?} 输出: $(printf '%s' "$HELP_BODY" | tr '\n' ' ' | head -c 300)"

if printf '%s' "$HELP_BODY" | grep -q -- '-listen'; then
    # --help 的 rc=2 是 Go flag 包行为(ErrHelp),不作失败判据。
    driver_verdict PASS "A13-2 插件二进制认识 ui 子命令(输出含 -listen;--help rc=${HELP_RC:-?},Go flag 包的 ErrHelp 行为,不作失败判据)"
elif printf '%s' "$HELP_BODY" | grep -qiE 'unknown (command|subcommand)|未知(命令|子命令)'; then
    driver_verdict FAIL "A13-2 插件 release 不认识 ui 子命令 —— 镜像拉到的插件 release 是否 ≥ v0.5.0?: $(printf '%s' "$HELP_BODY" | tr '\n' ' ' | head -c 300)"
elif [ "${HELP_RC:-}" = "124" ]; then
    driver_verdict FAIL "A13-2 'ui --help' 挂住被 timeout 砍掉(rc=124):旧版插件会把 ui 当 MCP stdio 模式跑 —— 镜像拉到的插件 release 是否 ≥ v0.5.0?: $(printf '%s' "$HELP_BODY" | tr '\n' ' ' | head -c 300)"
else
    driver_verdict FAIL "A13-2 'ui --help' 输出里没有 -listen(rc=${HELP_RC:-?}): $(printf '%s' "$HELP_BODY" | tr '\n' ' ' | head -c 300)"
fi

# ---------------------------------------------------------------------- A13-3
say "A13-3 · 起单元并取 HTTP 状态码"
START_OUT="$(gcmd "sudo systemctl start daedalus-blueprint-ui 2>&1; echo RC=\$?")"
START_RC="$(printf '%s' "$START_OUT" | sed -n 's/^RC=//p' | tail -1)"
START_BODY="$(printf '%s' "$START_OUT" | sed '/^RC=/d')"
info "systemctl start rc=${START_RC:-?} 输出: $(printf '%s' "$START_BODY" | tr '\n' ' ' | head -c 300)"
ACTIVE="$(gcmd "systemctl is-active daedalus-blueprint-ui")"
info "is-active: ${ACTIVE:-<空>}"

# 监听是 exec 之后才建立的:起得来不等于马上能连,给它几秒。
CODE=""
WAITED=0
while [ "$WAITED" -lt 15 ]; do
    CODE="$(http_code "$UI_URL" GET)"
    [ -n "$CODE" ] && break
    WAITED=$((WAITED + 1))
    sleep 1
done
info "GET $UI_URL → ${CODE:-<无响应>}"

A13_3_MISS=""
[ "${ACTIVE:-}" = "active" ] || A13_3_MISS="${A13_3_MISS} is-active=${ACTIVE:-<空>}(期望 active)"
[ "${CODE:-}" = "200" ] || A13_3_MISS="${A13_3_MISS} GET / 返回 ${CODE:-<无响应>}(期望 200)"

if [ -z "$A13_3_MISS" ]; then
    driver_verdict PASS "A13-3 单元起得来且真的在服务:is-active=active,GET / = 200"
else
    driver_verdict FAIL "A13-3 单元没真正服务起来:${A13_3_MISS}(start rc=${START_RC:-?},输出: $(printf '%s' "$START_BODY" | tr '\n' ' ' | head -c 300))"
    # 失败时把 journal 拉出来:「起不来」的原因只有日志里有(namespace 路径缺失
    # / 端口占用 / 插件二进制起不来,症状完全不同)。
    say "A13-3 失败诊断 · systemctl status / journalctl"
    gcmd "sudo systemctl status daedalus-blueprint-ui --no-pager -l 2>&1 | head -25" | sed 's/^/      /'
    gcmd "sudo journalctl -u daedalus-blueprint-ui --no-pager -n 25 2>&1 | tail -25" | sed 's/^/      /'
fi

# 诊断(只判 NOTE,不改 A13-3 的判定):镜像里若没有单元声明的 ReadWritePaths
# 目录,systemd 的 mount namespacing 会直接让单元起不来 —— 那与「UI 代码坏了」
# 是两回事,分开说清楚才不至于把结论归错因。
if [ -n "${A13_3_MISS:-}" ]; then
    say "A13-3b · 诊断:补建单元声明的输出目录后再试一次(区分「镜像缺目录」与「UI 起不来」)"
    MISSING="$(gcmd 'for d in /etc/nginx/conf.d /etc/haproxy /etc/postgresql /etc/redis; do [ -d "$d" ] || echo "$d"; done')"
    if [ -n "$MISSING" ]; then
        info "镜像里缺这些目录: $(printf '%s' "$MISSING" | tr '\n' ' ')"
        RETRY="$(gcmd 'for d in /etc/nginx/conf.d /etc/haproxy /etc/postgresql /etc/redis; do sudo mkdir -p "$d"; done; sudo systemctl restart daedalus-blueprint-ui 2>&1; sleep 3; systemctl is-active daedalus-blueprint-ui')"
        RETRY_CODE=""
        i=0
        while [ "$i" -lt 10 ]; do
            RETRY_CODE="$(http_code "$UI_URL" GET)"
            [ -n "$RETRY_CODE" ] && break
            i=$((i + 1))
            sleep 1
        done
        info "补建目录后: is-active=${RETRY:-<空>} GET / → ${RETRY_CODE:-<无响应>}"
        if [ "${RETRY_CODE:-}" = "200" ]; then
            driver_verdict NOTE "A13-3b 诊断:镜像未创建 ReadWritePaths 目录(/etc/nginx/conf.d 等),补建后单元可起且 GET / = 200 —— A13-3 失败的根因是镜像缺目录,不是 UI 代码"
        else
            driver_verdict NOTE "A13-3b 诊断:补建目录后仍起不来(is-active=${RETRY:-<空>},GET / → ${RETRY_CODE:-<无响应>}) —— 根因不在缺失的输出目录上"
        fi
    else
        driver_verdict NOTE "A13-3b 诊断:单元声明的输出目录都在,失败根因另有其人(见上面的 systemctl status / journalctl)"
    fi
fi

# ---------------------------------------------------------------------- A13-4
say "A13-4 · 四道防护里的两条(Host 白名单 / 自定义头)"
EVIL_CODE="$(http_code "$UI_URL" GET "evil.example")"
APPLY_CODE="$(http_code "${UI_URL}/api/apply" POST)"
info "GET / (Host: evil.example) → ${EVIL_CODE:-<无响应>}(期望 403)"
info "POST /api/apply (不带 X-Daedalus-Ui) → ${APPLY_CODE:-<无响应>}(期望 403)"

A13_4_MISS=""
[ "${EVIL_CODE:-}" = "403" ] || A13_4_MISS="${A13_4_MISS} Host: evil.example 返回 ${EVIL_CODE:-<无响应>}(期望 403)"
[ "${APPLY_CODE:-}" = "403" ] || A13_4_MISS="${A13_4_MISS} POST /api/apply 缺自定义头返回 ${APPLY_CODE:-<无响应>}(期望 403)"
if [ -z "$A13_4_MISS" ]; then
    driver_verdict PASS "A13-4 四道防护在真单元上成立:Host: evil.example → 403,POST /api/apply 缺 X-Daedalus-Ui → 403"
else
    driver_verdict FAIL "A13-4 四道防护未生效:${A13_4_MISS}"
fi

# ---------------------------------------------------------------------- A13-5
say "A13-5 · 沙箱真的落在单元上"
SHOW="$(gcmd "systemctl show -p DynamicUser -p ProtectSystem -p NoNewPrivileges -p SystemCallFilter daedalus-blueprint-ui 2>&1")"
printf '%s\n' "$SHOW" | sed 's/^/      /'
log "      A13-5 systemctl show: $(printf '%s' "$SHOW" | tr '\n' '|')"

A13_5_MISS=""
printf '%s' "$SHOW" | grep -q '^DynamicUser=yes' || A13_5_MISS="${A13_5_MISS} DynamicUser≠yes"
printf '%s' "$SHOW" | grep -q '^ProtectSystem=strict' || A13_5_MISS="${A13_5_MISS} ProtectSystem≠strict"
printf '%s' "$SHOW" | grep -q '^NoNewPrivileges=yes' || A13_5_MISS="${A13_5_MISS} NoNewPrivileges≠yes"
# systemd 会把 @system-service **展开成 syscall 清单**,`systemctl show` 里根本没有
# 字面量 "@system-service" —— 按字面量断言必然判红(真机第一次跑就栽在这里,而沙箱
# 其实好好的)。改成两段查:策略里声明了 + 运行时确实生效。
CAT_UNIT="$(gcmd "systemctl cat daedalus-blueprint-ui 2>&1")"
FILTER="$(printf '%s' "$SHOW" | sed -n 's/^SystemCallFilter=//p')"
printf '%s' "$CAT_UNIT" | grep -q '@system-service' \
    || A13_5_MISS="${A13_5_MISS} 单元策略里没有 @system-service(检查 drop-in 是否落地)"
[ -n "$FILTER" ] \
    || A13_5_MISS="${A13_5_MISS} SystemCallFilter 为空(策略没落到单元上)"
case "$FILTER" in
    *write*|*read*) ;;
    *) A13_5_MISS="${A13_5_MISS} SystemCallFilter 展开后不含 write/read(白名单没生效)" ;;
esac
if [ -z "$A13_5_MISS" ]; then
    driver_verdict PASS "A13-5 沙箱落在单元上:DynamicUser=yes / ProtectSystem=strict / NoNewPrivileges=yes / SystemCallFilter 声明 @system-service 且已展开生效"
else
    driver_verdict FAIL "A13-5 沙箱项缺失:${A13_5_MISS}(原始输出见上)"
fi

# ---------------------------------------------------------------------- 收尾
say "收尾 · 停掉单元(guest 是快照,但不留运行中单元)"
gcmd "sudo systemctl stop daedalus-blueprint-ui 2>&1; echo RC=\$?" | sed 's/^/      /'
info "停后 is-active: $(gcmd 'systemctl is-active daedalus-blueprint-ui')"

driver_exit
