#!/usr/bin/env bash
# daedalus-blueprint-ui 单元门禁:把「UI 与 MCP 是同一套判定、同一套沙箱」钉住。
#
# 三道检查:
#  ① 单元存在,ExecStart 走插件安装态的同一个二进制 + `ui` 子命令,监听地址必须是回环
#     —— 写成 0.0.0.0 等于把"本机运维界面"变成局域网服务,这一条不许靠人眼。
#  ② 沙箱 drop-in 的 [Service] 指令段与 daedalus-blueprint.service.d/landlock.conf 相等
#     —— 两条入口跑同一个 applyOnce、写同一批目录、spawn 同一批 post_check,沙箱面
#     不许因入口不同而弱一档。这类漂移不会报错,只会静默变松,故必须逐行比。
#  ③ 两份 CORE_UNITS(76 构建脚本 / plugin-inventory 门禁)都含 blueprint-ui 且彼此相等
#     —— 少一处,构建期的「插件单元集合 == CAPS」反向比对就会失败,但报错信息离真因很远。
#
# 用法:
#   blueprint-ui-unit.sh            校验仓库现状(任一检查不过即 exit 1)
#   blueprint-ui-unit.sh --selftest 证明本门禁没变瞎(逐条注入缺陷,必须被抓)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UNIT_DIR="$ROOT/files/system/usr/lib/systemd/system"
SCRIPT76="$ROOT/files/scripts/76-daedalus-plugin-gen.sh"
INVENTORY="$ROOT/tests/build/plugin-inventory.sh"

# section_directives <file> —— 只取 [Service] 段的指令行(去注释/空行,排序)。
# 排序后比较:本检查问的是"真下发的指令集合是否相同",不是书写顺序。
section_directives() {
    sed -n '/^\[Service\]/,$p' "$1" \
        | grep -v '^[[:space:]]*#' \
        | grep -v '^[[:space:]]*$' \
        | grep -v '^\[' \
        | sort
}

# core_units <script> —— 读该脚本里的 CORE_UNITS="..."(单一事实源,不另维护名单)。
core_units() {
    sed -n 's/^CORE_UNITS="\(.*\)"$/\1/p' "$1" | head -n 1 | tr ' ' '\n' | sed '/^$/d' | sort
}

# check <unit_dir> <script76> <inventory> —— 跑三道检查,逐条打印 FAIL,返回失败数。
check() {
    local unit_dir="$1" s76="$2" inv="$3"
    local unit="$unit_dir/daedalus-blueprint-ui.service"
    local ui_dropin="$unit_dir/daedalus-blueprint-ui.service.d/landlock.conf"
    local mcp_dropin="$unit_dir/daedalus-blueprint.service.d/landlock.conf"
    local fails=0

    # ① 单元 + ExecStart + 回环监听
    if [ ! -f "$unit" ]; then
        echo "FAIL 单元缺失: $unit"; fails=$((fails+1))
    else
        local exec_line listen host
        exec_line="$(sed -n 's/^ExecStart=//p' "$unit" | head -n 1)"
        if [ -z "$exec_line" ]; then
            echo "FAIL 单元没有 ExecStart: $unit"; fails=$((fails+1))
        elif ! printf '%s' "$exec_line" | grep -q 'daedalus-blueprint ui --listen '; then
            echo "FAIL ExecStart 不是 'daedalus-blueprint ui --listen ...': $exec_line"; fails=$((fails+1))
        else
            listen="$(printf '%s' "$exec_line" | sed -n 's/.*--listen \([^ ]*\).*/\1/p')"
            host="${listen%%:*}"
            case "$host" in
                127.0.0.1|localhost|::1) ;;
                *) echo "FAIL --listen 不是回环地址(实际 host=$host):$exec_line"; fails=$((fails+1)) ;;
            esac
        fi
    fi

    # ② 沙箱 drop-in 与 MCP 那份相等
    if [ ! -f "$ui_dropin" ]; then
        echo "FAIL 沙箱 drop-in 缺失: $ui_dropin"; fails=$((fails+1))
    elif [ ! -f "$mcp_dropin" ]; then
        echo "FAIL 参照 drop-in 缺失(无法比对): $mcp_dropin"; fails=$((fails+1))
    elif ! diff -u <(section_directives "$mcp_dropin") <(section_directives "$ui_dropin") >/dev/null; then
        echo "FAIL 沙箱 drop-in 与 daedalus-blueprint 那份不等(两入口沙箱漂移):"
        diff -u <(section_directives "$mcp_dropin") <(section_directives "$ui_dropin") | sed 's/^/     /' || true
        fails=$((fails+1))
    fi

    # ③ CORE_UNITS 双表
    local a b
    a="$(core_units "$s76")"
    b="$(core_units "$inv")"
    if [ -z "$a" ]; then
        echo "FAIL 读不到 $s76 的 CORE_UNITS"; fails=$((fails+1))
    elif ! printf '%s\n' "$a" | grep -qx 'blueprint-ui'; then
        echo "FAIL 76 脚本的 CORE_UNITS 缺 blueprint-ui(构建期反向集合比对会失败)"; fails=$((fails+1))
    fi
    if [ -z "$b" ]; then
        echo "FAIL 读不到 $inv 的 CORE_UNITS"; fails=$((fails+1))
    elif ! printf '%s\n' "$b" | grep -qx 'blueprint-ui'; then
        echo "FAIL plugin-inventory 的 CORE_UNITS 缺 blueprint-ui(该门禁会误报单元集合不等)"; fails=$((fails+1))
    fi
    if [ -n "$a" ] && [ -n "$b" ] && [ "$a" != "$b" ]; then
        echo "FAIL 两份 CORE_UNITS 不等:"; echo "     76=$a"; echo "     inv=$b"; fails=$((fails+1))
    fi

    return "$fails"
}

# --- 自检:逐条注入缺陷,证明每道检查都真的会红 ------------------------------
if [ "${1:-}" = "--selftest" ]; then
    # 自检要故意制造失败:check 返回非零是**期望结果**,不能让它触发 set -e。
    set +e
    fail_n=0
    st() { # st <期望失败数> <实际失败数> <说明>
        if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
        else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail_n=$((fail_n+1)); fi
    }

    printf '== blueprint-ui-unit.sh --selftest ==\n'
    s="$(mktemp -d)"
    trap 'rm -rf "$s"' EXIT

    # 基线夹具:一份"正确"的单元树 + 两份含 blueprint-ui 的脚本。
    mkdir -p "$s/units/daedalus-blueprint.service.d" "$s/units/daedalus-blueprint-ui.service.d"
    cat > "$s/units/daedalus-blueprint.service.d/landlock.conf" <<'EOF'
[Service]
# 参照
CPUQuota=50%
SystemCallFilter=@system-service
MemoryDenyWriteExecute=yes
EOF
    cat > "$s/units/daedalus-blueprint-ui.service.d/landlock.conf" <<'EOF'
[Service]
# UI 侧
MemoryDenyWriteExecute=yes
CPUQuota=50%
SystemCallFilter=@system-service
EOF
    cat > "$s/units/daedalus-blueprint-ui.service" <<'EOF'
[Unit]
Description=x
[Service]
ExecStart=/opt/daedalus/plugins/daedalus.blueprint/bin/daedalus-blueprint ui --listen 127.0.0.1:8787
[Install]
WantedBy=multi-user.target
EOF
    printf 'CAPS="fs blueprint"\nCORE_UNITS="audit blueprint-ui"\n' > "$s/76.sh"
    printf 'CORE_UNITS="audit blueprint-ui"\n' > "$s/inv.sh"

    st 0 "$(check "$s/units" "$s/76.sh" "$s/inv.sh" >/dev/null 2>&1; echo $?)" "基线夹具全绿(rc=0)"

    # ①-1 ExecStart 监听 0.0.0.0 → 必须抓
    cp "$s/units/daedalus-blueprint-ui.service" "$s/ui.bak"
    sed -i 's/127\.0\.0\.1:8787/0.0.0.0:8787/' "$s/units/daedalus-blueprint-ui.service"
    st 1 "$(check "$s/units" "$s/76.sh" "$s/inv.sh" 2>&1 | grep -c '不是回环地址')" "监听 0.0.0.0 必须被抓"
    cp "$s/ui.bak" "$s/units/daedalus-blueprint-ui.service"

    # ①-2 ExecStart 丢了 ui 子命令(退化成 MCP stdio 形态)→ 必须抓
    sed -i 's/ ui --listen [^ ]*//' "$s/units/daedalus-blueprint-ui.service"
    st 1 "$(check "$s/units" "$s/76.sh" "$s/inv.sh" 2>&1 | grep -c 'ExecStart 不是')" "缺 ui 子命令必须被抓"
    cp "$s/ui.bak" "$s/units/daedalus-blueprint-ui.service"

    # ② drop-in 少一条 seccomp/加固指令 → 必须抓(顺序不同不算漂移,上面基线已证明)
    sed -i '/MemoryDenyWriteExecute/d' "$s/units/daedalus-blueprint-ui.service.d/landlock.conf"
    st 1 "$(check "$s/units" "$s/76.sh" "$s/inv.sh" 2>&1 | grep -c '两入口沙箱漂移')" "drop-in 少一条指令必须被抓"
    cat > "$s/units/daedalus-blueprint-ui.service.d/landlock.conf" <<'EOF'
[Service]
MemoryDenyWriteExecute=yes
CPUQuota=50%
SystemCallFilter=@system-service
EOF

    # ③-1 76 脚本的 CORE_UNITS 缺 blueprint-ui → 必须抓
    printf 'CAPS="fs blueprint"\nCORE_UNITS="audit"\n' > "$s/76.sh"
    st 1 "$(check "$s/units" "$s/76.sh" "$s/inv.sh" 2>&1 | grep -c '76 脚本的 CORE_UNITS 缺')" "76 的 CORE_UNITS 缺项必须被抓"

    # ③-2 plugin-inventory 缺 blueprint-ui → 必须抓
    printf 'CAPS="fs blueprint"\nCORE_UNITS="audit blueprint-ui"\n' > "$s/76.sh"
    printf 'CORE_UNITS="audit"\n' > "$s/inv.sh"
    st 1 "$(check "$s/units" "$s/76.sh" "$s/inv.sh" 2>&1 | grep -c 'plugin-inventory 的 CORE_UNITS 缺')" "inventory 的 CORE_UNITS 缺项必须被抓"

    # ③-3 两份表互不相等 → 必须抓
    printf 'CORE_UNITS="audit env blueprint-ui"\n' > "$s/76.sh"
    printf 'CORE_UNITS="audit blueprint-ui"\n' > "$s/inv.sh"
    st 1 "$(check "$s/units" "$s/76.sh" "$s/inv.sh" 2>&1 | grep -c '两份 CORE_UNITS 不等')" "两份 CORE_UNITS 不等必须被抓"

    if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
    echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
fi

# --- 正式流程 ---------------------------------------------------------------
n=0
check "$UNIT_DIR" "$SCRIPT76" "$INVENTORY" || n=$?
if [ "$n" -ne 0 ]; then
    echo "blueprint-ui-unit: $n 项检查未通过" >&2
    exit 1
fi
echo "OK: blueprint-ui 单元与沙箱 drop-in 一致、监听回环、CORE_UNITS 双表齐备"
