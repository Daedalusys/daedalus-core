#!/usr/bin/env bash
# 76-daedalus-plugin-gen.sh —— 构建期插件单元渲染 + 策略一致性 + 自校验(计划 todo 9/13,决策 16/22)。
#
# 职责:
#   1. 逐个读取镜像内安装态插件 /opt/daedalus/plugins/daedalus.<cap>/daedalus.plugin.json,
#      调 daedalus-host verify 做零放宽完整性校验(sha256 逐条目 + manifest 规范化自摘要 + 可执行位);
#   2. 调 daedalus-host render-unit 从 manifest 渲染 ExecStart 行,写回(幂等)并复读确认
#      ——单元内容完全来自 manifest,消除手工漂移;systemd 才是进程父,宿主只渲染不 spawn;
#   3. 交叉核对 manifest.tools 与二进制真实暴露的 MCP tools/list(stdio 握手),不一致即构建失败;
#   4. 沙箱语义防回归:单元主体的 DynamicUser/ProtectSystem/NoNewPrivileges 与
#      .service.d/{landlock,credentials}.conf drop-in 必须原样存在,本脚本一个字节都不改它们;
#   5. 策略单一事实源(todo 13):预检 /opt/daedalus/shared/policy.toml 的存在性、必需节与
#      [shell].allowed_commands 集合,并把 DAEDALUS_POLICY_PATH 注入能力服务器启动握手——
#      显式指向的文件损坏/含未知键/字段缺失时,internal/policy 的 Go 解析器 fail-closed
#      拒绝启动,握手随之失败,即由真实解析器(而非 bash 近似)完成 TOML 合法性校验;
#   6. 策略消费约定(todo 13,拍板为"运行时读取"):单元**不渲染** Environment=ALLOW_COMMANDS,
#      Go 服务器启动时读取 policy.toml;若单元显式携带该覆盖,则必须与 [shell].allowed_commands
#      逐字一致(集合相等),否则构建失败——单元不得成为第二事实源;其余能力单元注入
#      ALLOW_COMMANDS 或用 DAEDALUS_POLICY_PATH 改指策略路径一律视为漂移,直接失败;
#   7. DynamicUser 策略可读性(todo 13):ProtectSystem=strict 只是把 /opt 挂成只读,
#      不拦截读取;DynamicUser 能否读到 policy.toml 取决于文件 o+r 位与祖先目录 o+x 位,
#      逐项验证即可,**无需**任何 BindReadOnlyPaths/ReadWritePaths 放宽(放宽反而破坏
#      "整盘只读 + 单点策略"的沙箱语义)。不满足即构建失败,防止服务器开机 fail-closed。
#
# 失败语义(对抗面):宿主二进制缺失、manifest 缺失/损坏/被篡改、checksums 不符、
# executable 不存在或缺可执行位、tools 漂移、ExecStart 复读不一致、policy.toml 缺失/损坏、
# 单元 ALLOW_COMMANDS 与策略漂移、策略对 DynamicUser 不可读 —— 一律 exit 1,构建失败。
#
# 开发态模拟:DAEDALUS_PLUGIN_GEN_ROOT=<暂存根> 可把四个绝对路径前缀
# (plugins/单元目录/宿主二进制/shared 策略)整体重定位,
# 用于不构建镜像而完整演练本脚本(镜像内构建时不设该变量,默认 /)。
set -euo pipefail

ROOT="${DAEDALUS_PLUGIN_GEN_ROOT:-/}"
PLUGINS="${ROOT%/}/opt/daedalus/plugins"
UNIT_DIR="${ROOT%/}/usr/lib/systemd/system"
HOST="${ROOT%/}/usr/local/bin/daedalus-host"
POLICY="${ROOT%/}/opt/daedalus/shared/policy.toml"
CAPS="fs shell pkg sysinfo"

fail() { echo "76-daedalus-plugin-gen: 错误: $*" >&2; exit 1; }

[ -x "$HOST" ] || fail "宿主二进制缺失或不可执行: $HOST(插件接线必须先安装 daedalus-host)"

# manifest_tools <清单文件> —— 抽取 tools 数组元素并排序。
# 安装包内 manifest 由打包器 MarshalIndent 生成(多行数组),源目录手写为单行;
# 先把全文压成一行再截 "tools": [...] 段,两种排版统一处理。
manifest_tools() {
    tr -d '\n' < "$1" \
        | sed -n 's/.*"tools": \[\([^]]*\)\].*/\1/p' \
        | tr ',' '\n' | tr -d ' "' | sed '/^$/d' | sort
}

# binary_tools <可执行文件> —— 通过 MCP stdio 握手(initialize → initialized → tools/list)
# 取回二进制实际注册的工具名并排序。sleep 保活防 stdin 提前 EOF 掐死响应(见 notepad todo 4)。
# 服务器进程携带 DAEDALUS_POLICY_PATH(见下方策略预检),故握手成功同时证明
# Go 解析器接受了该策略文件——损坏策略在 main() 的 applyPolicy 即 os.Exit(1)。
binary_tools() {
    { printf '%s\n' \
        '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"plugin-gen","version":"0"}}}' \
        '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
        '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
      sleep 2; } \
        | timeout 30 "$1" 2>/dev/null \
        | grep -m1 '"tools":\[' \
        | grep -o '"name":"[^"]*"' | sed 's/^"name":"//; s/"$//' | sort
}

# norm_csv —— 从 stdin 读逗号分隔列表,去引号/空白、丢空项、排序,得集合规范形。
norm_csv() {
    tr ',' '\n' | tr -d ' "' | sed '/^$/d' | sort
}

# policy_toml_list <键名> <策略文件> —— 抽取单行 TOML 数组值(如 allowed_commands)。
# policy.toml 由人手工维护,数组恒为单行排版;先压平全文再截取 "键 = [ ... ]" 段,
# 与 manifest_tools 同款手法。取不到即空串,由调用方判损坏。
policy_toml_list() {
    tr -d '\n' < "$2" \
        | sed -n "s/.*${1}[[:space:]]*=[[:space:]]*\[\([^]]*\)\].*/\1/p" \
        | norm_csv
}

# —— 0) 策略单一事实源预检(todo 13):存在性 → 必需节/键 → shell 白名单集合非空 ——
[ -f "$POLICY" ] || fail "policy.toml 缺失: $POLICY(单一事实源必须随镜像落位)"
[ -s "$POLICY" ] || fail "policy.toml 为空文件: $POLICY"
for sec in '^\[shell\]' '^\[fs\]' '^\[audit\]'; do
    grep -q "$sec" "$POLICY" || fail "policy.toml 损坏: 缺节 $sec (期望 [shell]/[fs]/[audit] 三节齐备)"
done
grep -Eq '^[[:space:]]*allowed_commands[[:space:]]*=' "$POLICY" || fail "policy.toml 损坏: [shell] 缺 allowed_commands"
grep -Eq '^[[:space:]]*allowed_dirs[[:space:]]*=' "$POLICY" || fail "policy.toml 损坏: [fs] 缺 allowed_dirs"
grep -Eq '^[[:space:]]*log_path[[:space:]]*=' "$POLICY" || fail "policy.toml 损坏: [audit] 缺 log_path"
POLICY_ALLOW="$(policy_toml_list allowed_commands "$POLICY")"
[ -n "$POLICY_ALLOW" ] || fail "policy.toml 损坏: [shell].allowed_commands 无法解析出任何命令项"

# —— 0a) DynamicUser 策略可读性(todo 13):ProtectSystem=strict 不拦读,
# 读取可行性 = 文件 o+r 位 + ROOT 子树内祖先目录 o+x 位(DynamicUser 以
# world/other 身份访问)。不满足即构建失败,而非部署后开机 fail-closed。
# 无需也不允许在此加 BindReadOnlyPaths 类放宽——只读整盘语义保持原样。
pol_mode="$(stat -c '%a' "$POLICY")"
[ $(( 8#$pol_mode & 8#004 )) -ne 0 ] \
    || fail "policy.toml 缺 o+r 位(mode $pol_mode):DynamicUser 无法读取,服务器将拒绝启动: $POLICY"
pol_stop="${ROOT%/}"
pol_dir="$(dirname "$POLICY")"
while [ "$pol_dir" != "$pol_stop" ] && [ "$pol_dir" != "/" ]; do
    d_mode="$(stat -c '%a' "$pol_dir")"
    [ $(( 8#$d_mode & 8#001 )) -ne 0 ] \
        || fail "policy.toml 祖先目录缺 o+x 位(mode $d_mode):DynamicUser 无法穿越: $pol_dir"
    pol_dir="$(dirname "$pol_dir")"
done

# 显式指向策略:让 shell/fs 二进制的启动握手顺带充当"真实解析器可加载性"校验
# (TOML 语法损坏/未知键/字段缺失 → internal/policy fail-closed → 服务器拒启 → 握手失败)。
export DAEDALUS_POLICY_PATH="$POLICY"
echo "76-daedalus-plugin-gen: policy.toml 预检通过($(echo "$POLICY_ALLOW" | wc -l) 项 shell 白名单,DynamicUser 可读)"

for cap in $CAPS; do
    id="daedalus.${cap}"
    manifest="$PLUGINS/$id/daedalus.plugin.json"
    unit="$UNIT_DIR/daedalus-$cap.service"
    dropin_dir="$UNIT_DIR/daedalus-$cap.service.d"

    [ -f "$manifest" ] || fail "$id: manifest 缺失: $manifest"
    [ -f "$unit" ] || fail "$id: systemd 单元缺失: $unit"

    # —— 1) manifest 完整性(损坏/篡改/缺 checksums → host 非零退出)——
    "$HOST" verify "$id" -dir "$PLUGINS" || fail "$id: daedalus-host verify 失败(manifest 损坏或文件被篡改)"

    # —— 2) 从 manifest 渲染 ExecStart(degraded 插件 render-unit 拒绝产出)——
    rendered="$("$HOST" render-unit "$id" -dir "$PLUGINS" | grep '^ExecStart=' || true)"
    [ -n "$rendered" ] || fail "$id: render-unit 未产出 ExecStart 行"
    exe="${rendered#ExecStart=}"
    case "$exe" in
        "$PLUGINS/$id/"*) ;; # 路径必须落在该插件安装目录内,防 manifest 越界声明
        *) fail "$id: 渲染出的可执行路径 $exe 不在插件目录 $PLUGINS/$id 内" ;;
    esac
    [ -f "$exe" ] || fail "$id: manifest executable 声明的文件不存在: $exe"
    [ -x "$exe" ] || fail "$id: 可执行文件缺可执行位: $exe"

    # —— 3) manifest.tools 与二进制 tools/list 逐项比对(计数 + 集合)——
    want="$(manifest_tools "$manifest")"
    [ -n "$want" ] || fail "$id: manifest tools 为空,能力服务器必须声明工具"
    got="$(binary_tools "$exe")" || fail "$id: 二进制未应答 tools/list(启动失败、30s 超时,或 DAEDALUS_POLICY_PATH 指向的策略被 Go 解析器拒绝): $exe"
    [ "$want" = "$got" ] || fail "$id: tools 与二进制不符 —— manifest{$(echo "$want" | tr '\n' ' ')} vs 二进制{$(echo "$got" | tr '\n' ' ')}"

    # —— 4) 沙箱语义防回归:单元主体行 + drop-in 必须原样存在 ——
    grep -qx 'DynamicUser=yes' "$unit" || fail "$id: 单元主体丢失 DynamicUser=yes(沙箱语义不可变)"
    grep -qx 'ProtectSystem=strict' "$unit" || fail "$id: 单元主体丢失 ProtectSystem=strict(沙箱语义不可变)"
    grep -qx 'NoNewPrivileges=yes' "$unit" || fail "$id: 单元主体丢失 NoNewPrivileges=yes(沙箱语义不可变)"
    for d in landlock credentials; do
        [ -f "$dropin_dir/$d.conf" ] || fail "$id: 沙箱 drop-in 缺失: $dropin_dir/$d.conf"
    done
    grep -q 'SystemCallFilter=@system-service' "$dropin_dir/landlock.conf" || fail "$id: landlock.conf 丢失 seccomp 过滤行"
    grep -q 'LoadCredential=daedalus_token' "$dropin_dir/credentials.conf" || fail "$id: credentials.conf 丢失 LoadCredential 行"

    # —— 5) 幂等写回 + 复读自校验:单元必须恰有一行 ExecStart,写后与渲染逐字相等 ——
    n="$(grep -c '^ExecStart=' "$unit" || true)"
    [ "$n" = "1" ] || fail "$id: 单元应恰有一行 ExecStart,实际 $n 行: $unit"
    sed -i "s|^ExecStart=.*|${rendered}|" "$unit"
    grep -Fxq "$rendered" "$unit" || fail "$id: 写回后复读与渲染不符(单元写入异常): $rendered"

    # —— 6) 策略消费一致性(todo 13,约定 = 运行时读取):
    # 单元不渲染 Environment=ALLOW_COMMANDS,白名单唯一来源是 policy.toml;
    # 若单元显式携带该覆盖,值必须与 [shell].allowed_commands 集合逐字相等,
    # 否则单元就成了第二事实源,构建失败。ALLOW_COMMANDS 只对 shell 有意义,
    # 注入其余能力单元同样视为漂移。DAEDALUS_POLICY_PATH 由部署/测试注入,
    # 不得固化进单元(否则镜像内策略生产路径被悄悄改指)。
    # 两种 systemd 写法都识别:Environment=KEY=... 与 Environment="KEY=..."。 ——
    policy_env="$(grep -E '^Environment="?(DAEDALUS_POLICY_PATH|ALLOW_COMMANDS)=' "$unit" || true)"
    case "$policy_env" in
        *DAEDALUS_POLICY_PATH*)
            fail "$id: 单元不得固化 Environment=DAEDALUS_POLICY_PATH(生产策略路径 /opt/daedalus/shared/policy.toml 由运行时解析)" ;;
    esac
    env_line="$(printf '%s\n' "$policy_env" | grep '^Environment="\?ALLOW_COMMANDS=' || true)"
    if [ "$cap" = shell ]; then
        if [ -n "$env_line" ]; then
            # 剥 Environment= 前缀 → 剥可选前引号 → 剥键名= → 剥可选尾引号;
            # norm_csv 再统一去空格/引号,得到与 POLICY_ALLOW 可比的集合规范形。
            env_val="${env_line#Environment=}"; env_val="${env_val#\"}"
            env_val="${env_val#ALLOW_COMMANDS=}"; env_val="${env_val%\"}"
            [ -n "$env_val" ] || fail "$id: Environment=ALLOW_COMMANDS 为空值——约定是运行时读取策略,要么删除该行要么逐字等于 [shell].allowed_commands"
            env_set="$(printf '%s' "$env_val" | norm_csv)"
            [ "$env_set" = "$POLICY_ALLOW" ] \
                || fail "$id: 单元 ALLOW_COMMANDS{$(echo "$env_set" | tr '\n' ' ')} 与 policy.toml [shell].allowed_commands{$(echo "$POLICY_ALLOW" | tr '\n' ' ')} 不一致(REPLACE 语义下单元覆盖即第二事实源,禁止)"
        fi
        # env_line 为空 = 无覆盖 → 运行时读取 policy.toml,即本约定常态。
    else
        [ -z "$env_line" ] || fail "$id: ALLOW_COMMANDS 仅 daedalus-shell 消费,注入 $id 单元属漂移: $env_line"
    fi

    # —— 7) 策略可读性防回归(shell/fs):不得出现遮蔽/重绑定 /opt 的挂载指令 ——
    # ProtectSystem=strict 只读不拦读;TemporaryFileSystem=/opt…(遮蔽空挂)、
    # InaccessiblePaths=/opt…(拒读)、BindPaths=/opt…(改写绑定)都会切断
    # DynamicUser 对 policy.toml 的读取 → 服务器 fail-closed 拒启。此处提前拦截;
    # BindReadOnlyPaths/ReadWritePaths 不拦读取,刻意不在禁止之列。
    if [ "$cap" = shell ] || [ "$cap" = fs ]; then
        if grep -Eq '^(TemporaryFileSystem=|InaccessiblePaths=|BindPaths=).*/opt' "$unit" "$dropin_dir"/*.conf 2>/dev/null; then
            fail "$id: 出现遮蔽/重绑定 /opt 的挂载指令,会切断 policy.toml 读取(见 76 脚本注释第 7 条)"
        fi
    fi

    echo "76-daedalus-plugin-gen: $id 渲染通过 -> $rendered"
done

echo "76-daedalus-plugin-gen: 全部 ${CAPS} 能力插件单元渲染、策略一致性与自校验通过"
