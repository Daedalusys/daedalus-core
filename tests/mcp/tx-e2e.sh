#!/usr/bin/env bash
# =============================================================================
# tx-e2e.sh —— 外部 AI agent 受控变更通道的**真机**端到端验收
# =============================================================================
#
# 验的是三档判定的真实行为(不是模拟):
#   第一档 auto   —— AI 自己就执行,单元真的起来;
#   第二档 confirm —— 没条子被拒并给出所需范围;补一张条子后放行;条子绑人;
#   第三档 human  —— AI 出待办、**没有**批准记录就不执行;人批准后按同一 argv
#                    执行一次,再用就作废。
#
# 为什么是「真机」:这套通道的判定依赖 `systemctl --user`(要求调用者在活着的
# 用户会话里)与真实的 /run 目录权限。真机才有这些;宿主上的假 systemctl 验不了。
#
# 第二个用户是**真用户**(家目录 + login shell + linger 起的 user manager):
# 待办按 uid 分目录、批准按请求者核对、用量账本按人分文件 —— 这三件事只有在
# 「两个用户各自有自己的 user 域」时才算真验过。上一轮用 nologin + `sudo -u`
# 的临时用户只验了绑定判定,验不到「各自一条 user bus」。
#
# 用法(在目标机上,普通用户身份,sudo 免密):
#   tx-e2e.sh --bin-dir <含 daedalus-tx 与 daedalus-tx-mcp 的目录> \
#             [--policy <policy.toml 路径>] [--evidence <输出 md 路径>]
#
# 环境变量:
#   E2E_UNIT_PREFIX=daedalus-tx-e2e     测试单元名前缀(默认)
#   E2E_AUTONOMY_PATH=/etc/daedalus/agent-autonomy.toml
#   E2E_GRANTS_DIR=/run/daedalus/grants
#   E2E_PENDING_DIR=/run/daedalus/pending   (根目录;每个用户是其中的 <uid> 子目录)
#   E2E_APPROVALS_DIR=/run/daedalus/approvals
#   E2E_CONSUMED_DIR=/run/daedalus/consumed (根目录;每个用户是其中的 <uid> 子目录)
#   E2E_SECOND_USER=daedalus-e2e-other     第二个用户(不存在则本次创建,用完删掉)
# =============================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLIENT="${E2E_CLIENT:-$REPO_ROOT/tests/mcp/tx-e2e-client.py}"

BIN_DIR=""
POLICY=""
EVIDENCE=""
UNIT_PREFIX="${E2E_UNIT_PREFIX:-daedalus-tx-e2e}"
AUTONOMY_PATH="${E2E_AUTONOMY_PATH:-/etc/daedalus/agent-autonomy.toml}"
GRANTS_DIR="${E2E_GRANTS_DIR:-/run/daedalus/grants}"
PENDING_DIR="${E2E_PENDING_DIR:-/run/daedalus/pending}"
APPROVALS_DIR="${E2E_APPROVALS_DIR:-/run/daedalus/approvals}"
CONSUMED_DIR="${E2E_CONSUMED_DIR:-/run/daedalus/consumed}"
# 第二个用户用来验「绑定到别人的条子用不了」「待办按人分目录」「批准按请求者核对」
# 「每人一本账」。默认本次创建,用完连同家目录一起删掉。
SECOND_USER="${E2E_SECOND_USER:-daedalus-e2e-other}"
SECOND_CREATED=0
SECOND_UID=""
SECOND_HOME_DIR=""
SECOND_RUNTIME_DIR=""
SECOND_BUS=""

while [ $# -gt 0 ]; do
  case "$1" in
    --bin-dir) BIN_DIR="$2"; shift 2 ;;
    --policy) POLICY="$2"; shift 2 ;;
    --evidence) EVIDENCE="$2"; shift 2 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

[ -n "$BIN_DIR" ] || { echo "--bin-dir 必填" >&2; exit 2; }
TX_BIN="$BIN_DIR/daedalus-tx"
MCP_BIN="$BIN_DIR/daedalus-tx-mcp"
[ -x "$TX_BIN" ] || { echo "缺少可执行: $TX_BIN" >&2; exit 2; }
[ -x "$MCP_BIN" ] || { echo "缺少可执行: $MCP_BIN" >&2; exit 2; }
[ -f "$CLIENT" ] || { echo "缺少客户端: $CLIENT" >&2; exit 2; }

mkdir -p "$(dirname "${EVIDENCE:-$PWD/tx-e2e-evidence.md}")"
EV="${EVIDENCE:-$PWD/tx-e2e-evidence.md}"
: > "$EV"

PASS=0
FAIL=0
declare -a FAILURES=()

say() { printf '%s\n' "$*" >> "$EV"; }
step() { say ""; say "## $*"; }
note() { say "- $*"; }
check() { # check <名称> <期望> <实际>
  local name="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    PASS=$((PASS+1)); say "- ✅ $name"
  else
    FAIL=$((FAIL+1)); FAILURES+=("$name: 期望 [$want] 实际 [$got]")
    say "- ❌ $name —— 期望 [$want] 实际 [$got]"
  fi
}
run() { # run <说明> <命令...> —— 记录命令与退出码,返回值即退出码
  local desc="$1"; shift
  say ""
  say "\`$ $* \`"
  local out rc
  out="$("$@" 2>&1)"; rc=$?
  say ""
  say '```'
  say "$out"
  say '```'
  say "退出码: $rc"
  return $rc
}
show_raw() { # show_raw <原始输出> <退出码> —— 把一次命令的输出原样留进证据
  say ""
  say '```'
  say "$1"
  say '```'
  say "退出码: $2"
}
finish() {
  say ""
  say "## 汇总"
  say "- 通过 $PASS,失败 $FAIL"
  for f in "${FAILURES[@]:-}"; do [ -n "$f" ] && say "- ❌ $f"; done
  say ""
  say "证据文件: $EV"
  [ "$FAIL" -eq 0 ]
}

# cleanup_second_user 只清本次创建的用户:调用方用 E2E_SECOND_USER 指向既有用户时
# 一律不动。提前失败(比如会话没起来)也必须走这里 —— 否则临时用户会留在机器上,
# 下一轮就变成"既有用户",再也删不掉。
cleanup_second_user() {
  [ "${SECOND_CREATED:-0}" = 1 ] || return 0
  as_second systemctl --user disable --now "$UNIT_CONFIRM" "$UNIT_HUMAN" >/dev/null 2>&1 || true
  sudo loginctl disable-linger "$SECOND_USER" >/dev/null 2>&1 || true
  sudo systemctl stop "user@$SECOND_UID.service" >/dev/null 2>&1 || true
  # 待办/消费标记落在 tmpfs 里,但 uid 会被下一个新用户复用:留着会让
  # 「目录属主不是本人」在下一轮变成莫名其妙的 fail-closed。
  sudo rm -rf "$PENDING_DIR/$SECOND_UID" "$CONSUMED_DIR/$SECOND_UID" 2>/dev/null || true
  sleep 1
  sudo userdel -r "$SECOND_USER" 2>/dev/null || true
}

UNIT_AUTO="$UNIT_PREFIX-auto.service"
UNIT_CONFIRM="$UNIT_PREFIX-confirm.service"
UNIT_HUMAN="$UNIT_PREFIX-human.service"
UNIT_DANGER="sshd-e2e.service"

mcp_call() { # mcp_call <工具> <参数 JSON> —— 输出 JSON 文本
  local extra=()
  [ -n "$POLICY" ] && extra+=(--env "DAEDALUS_POLICY_PATH=$POLICY")
  python3 "$CLIENT" --bin "$MCP_BIN" "${extra[@]}" --call "$1" --args "$2" 2>/dev/null
}

# as_second 以第二个用户的身份执行命令,并把它**自己的**会话环境给齐:
# 执行者身份来自进程 euid,而 systemctl --user 要的是该用户自己的运行时目录与
# user bus —— 少了这两个,命令会落到"无会话"的那条老路上(上一轮的形态)。
as_second() {
  sudo -n -u "$SECOND_USER" env HOME="$SECOND_HOME_DIR" \
    XDG_RUNTIME_DIR="$SECOND_RUNTIME_DIR" \
    DBUS_SESSION_BUS_ADDRESS="$SECOND_BUS" "$@"
}

# mcp_call_as 以另一个用户身份问同一台服务器:换用户 = 换执行者。
# 客户端与二进制要放在家目录之外的世界可读路径:目标用户进不了调用者的家目录(0700)。
mcp_call_as() { # mcp_call_as <用户> <工具> <参数 JSON>
  local extra=()
  [ -n "$POLICY" ] && extra+=(--env "DAEDALUS_POLICY_PATH=$SECOND_STAGE/policy.toml")
  as_second python3 "$SECOND_STAGE/tx-e2e-client.py" --bin "$SECOND_STAGE/daedalus-tx-mcp" \
    "${extra[@]}" --call "$2" --args "$3" 2>/dev/null
}

json_get() { # json_get <json> <表达式: result.content[0].text 里 JSON 的键路径>
  python3 - "$1" "$2" <<'PY'
import json, sys
raw, path = sys.argv[1], sys.argv[2]
try:
    outer = json.loads(raw)
except Exception:
    print(""); raise SystemExit(0)
text = ""
try:
    text = outer["result"]["content"][0]["text"]
except Exception:
    pass
try:
    inner = json.loads(text)
except Exception:
    print(text.strip()[:200]); raise SystemExit(0)
cur = inner
for part in path.split("."):
    if part == "":
        continue
    if isinstance(cur, list):
        try:
            cur = cur[int(part)]
            continue
        except Exception:
            print(""); raise SystemExit(0)
    if not isinstance(cur, dict) or part not in cur:
        print(""); raise SystemExit(0)
    cur = cur[part]
print(cur if not isinstance(cur, (dict, list)) else json.dumps(cur, ensure_ascii=False))
PY
}

# pl_field 从 `daedalus-tx pending list` 的 JSON 里取某条待办的字段。
# pending list 是全用户的视图(按请求者归属显示),所以它的键路径与单条 show 不同。
pl_field() { # pl_field <json> <pending-id> <字段>
  python3 - "$1" "$2" "$3" <<'PY'
import json, sys
raw, pid, field = sys.argv[1], sys.argv[2], sys.argv[3]
try:
    doc = json.loads(raw)
except Exception:
    print(""); raise SystemExit(0)
for entry in doc.get("pending", []):
    if entry.get("id") == pid:
        val = entry.get(field, "")
        print(val if not isinstance(val, (dict, list)) else json.dumps(val, ensure_ascii=False))
        raise SystemExit(0)
print("")
PY
}

# jq_file 从一份 JSON 文件里取顶层字段(批准记录/条子这类单份文件)。
jq_file() { # jq_file <路径> <键>
  python3 - "$1" "$2" <<'PY'
import json, sys
try:
    doc = json.load(open(sys.argv[1]))
except Exception:
    print(""); raise SystemExit(0)
val = doc.get(sys.argv[2], "")
print(val if not isinstance(val, (dict, list)) else json.dumps(val, ensure_ascii=False))
PY
}

# ---------------------------------------------------------------- preflight
step "preflight:环境能力"
note "uid=$(id -u) user=$(id -un) systemd=$(systemctl --version | head -1)"
if systemctl --user list-units >/dev/null 2>&1; then
  note "systemctl --user 可用(有活着的用户会话)"
  check "preflight:systemctl --user" ok ok
else
  check "preflight:systemctl --user" ok unavailable
  say "用户会话不可用 ⇒ systemctl --user 无处可去,后续场景无法验真"
  finish
  exit 1
fi
if sudo -n true 2>/dev/null; then
  note "sudo 免密可用"
else
  say "sudo 不可免密 ⇒ 签发/批准/目录这些 root 侧动作验不了"
fi

# ---------------------------------------------------------------- 目录与身份
step "准备:/run/daedalus 目录与签发者组"
sudo install -d -m 0755 -o root -g root /run/daedalus
sudo getent group daedalus-signers >/dev/null 2>&1 || sudo groupadd -f daedalus-signers
sudo install -d -m 1775 -o root -g daedalus-signers "$GRANTS_DIR"
# 待办根目录 1777(粘滞位,同 /tmp 语义):每个用户在里面建自己的 <uid> 子目录。
sudo install -d -m 1777 -o root -g root "$PENDING_DIR"
sudo install -d -m 0755 -o root -g root "$APPROVALS_DIR"
# 消费标记的父目录也必须由部署建出来且对所有用户可写:插件要在里面建自己的
# <uid> 子目录,父目录缺失时第三档会 fail-closed(状态变成"批准用过但执行不了")。
sudo install -d -m 1777 -o root -g root "$CONSUMED_DIR"
sudo usermod -aG daedalus-signers "$(id -un)" 2>/dev/null || true

# ---------------------------------------------------------------- 第二用户
step "准备:第二个用户(真用户 + 真会话)"
if ! getent passwd "$SECOND_USER" >/dev/null 2>&1; then
  # 家目录与 login shell 都要:user manager 要在家目录里读单元文件与写状态。
  sudo useradd -m -s /bin/bash "$SECOND_USER" && SECOND_CREATED=1
fi
SECOND_UID="$(id -u "$SECOND_USER" 2>/dev/null || true)"
if [ -z "$SECOND_UID" ]; then
  check "准备:第二用户就位" yes no
  finish
  exit 1
fi
SECOND_HOME_DIR="$(getent passwd "$SECOND_USER" | cut -d: -f6)"
[ -n "$SECOND_HOME_DIR" ] || SECOND_HOME_DIR="/home/$SECOND_USER"
SECOND_RUNTIME_DIR="/run/user/$SECOND_UID"
SECOND_BUS="unix:path=$SECOND_RUNTIME_DIR/bus"
note "第二用户: $SECOND_USER(uid=$SECOND_UID home=$SECOND_HOME_DIR)"

# linger 让 user manager 在没有登录会话时也活着 —— 这是「第二个用户真的有自己
# 一条 user bus」的前提。只在本次创建的用户上打开,既有用户一律不动。
if [ "$SECOND_CREATED" = 1 ]; then
  run "打开第二用户的 linger" sudo loginctl enable-linger "$SECOND_USER" || true
fi
# 既有会话就复用;没有就把 user manager 起起来(不动用户本身)。
sudo -n systemctl start "user@$SECOND_UID.service" >/dev/null 2>&1 || true
SESSION_OK=no
for _ in $(seq 1 30); do
  # 判定必须**以第二用户身份**做:/run/user/<uid> 是 0700,调用者连 stat 那个
  # socket 都会被拒 —— 用调用者的身份探,永远探不到自己刚起的会话。
  if as_second test -S "$SECOND_RUNTIME_DIR/bus" &&
     as_second systemctl --user show-environment >/dev/null 2>&1; then
    SESSION_OK=yes
    break
  fi
  sleep 1
done
check "准备:第二用户的 user bus 可用" yes "$SESSION_OK"
if [ "$SECOND_CREATED" = 1 ]; then
  check "准备:第二用户 Linger=yes" "Linger=yes" \
    "$(loginctl show-user "$SECOND_USER" 2>/dev/null | grep -m1 '^Linger=' || echo missing)"
fi
if [ "$SESSION_OK" != yes ]; then
  say "第二用户没有可用的 user 会话 ⇒ 待办分目录/批准归属/账本分册这些场景无法验真"
  cleanup_second_user
  finish
  exit 1
fi

# 目标用户进不了调用者的家目录(0700),所以客户端与二进制要另放一份世界可读的。
SECOND_STAGE="${TMPDIR:-/tmp}/daedalus-e2e-second-stage"
rm -rf "$SECOND_STAGE"
mkdir -p "$SECOND_STAGE"
cp "$CLIENT" "$SECOND_STAGE/tx-e2e-client.py"
cp "$MCP_BIN" "$SECOND_STAGE/daedalus-tx-mcp"
chmod 0644 "$SECOND_STAGE/tx-e2e-client.py"
chmod 0755 "$SECOND_STAGE/daedalus-tx-mcp" "$SECOND_STAGE"
# 策略文件也要放到世界可读的位置:目标用户连调用者家目录都进不去,
# 给它原路径等于让它读不到策略、直接拒启。
if [ -n "$POLICY" ]; then
  cp "$POLICY" "$SECOND_STAGE/policy.toml"
  chmod 0644 "$SECOND_STAGE/policy.toml"
fi

# 上一次跑留下的待办/批准/条子/消费标记会污染本次判定(批准在有效期内仍会被认;
# 消费标记会让"第一次使用"直接变成已消费)—— 全清,包括其他用户的子目录。
sudo rm -rf "$PENDING_DIR"/* "$CONSUMED_DIR"/* 2>/dev/null || true
sudo rm -f "$APPROVALS_DIR"/*.json "$GRANTS_DIR"/*.json 2>/dev/null || true
ls -ld /run/daedalus "$GRANTS_DIR" "$PENDING_DIR" "$APPROVALS_DIR" "$CONSUMED_DIR" >> "$EV"

# ---------------------------------------------------------------- 档位表
step "准备:档位表 $AUTONOMY_PATH"
sudo install -d -m 0755 -o root -g root /etc/daedalus
TMP_AUTO="$(mktemp)"
cat > "$TMP_AUTO" <<EOF
# 真机 E2E 档位表 —— 三个测试单元各走一档。
[[rule]]
scope = "systemd:user:start:$UNIT_AUTO"
tier = "auto"

[[rule]]
scope = "systemd:user:restart:$UNIT_CONFIRM"
tier = "confirm"

[[rule]]
scope = "systemd:user:restart:$UNIT_HUMAN"
tier = "human"

# stop 在 SDK 的默认表里是第二档;本轮"参数变了"那条断言要在第三档上验,
# 所以显式把它也定成第三档(E2E 自己的档位表,不涉及产品默认值)。
[[rule]]
scope = "systemd:user:stop:$UNIT_HUMAN"
tier = "human"

# 非 root 签发者:范围/时长/次数都有上限(超出即拒)。
[[signer]]
user = "$(id -un)"
max_scope = ["systemd:user:restart:$UNIT_CONFIRM"]
max_ttl = "1h"
max_uses = 5
EOF
sudo install -m 0644 -o root -g root "$TMP_AUTO" "$AUTONOMY_PATH"
rm -f "$TMP_AUTO"
note "档位表已装: auto=$UNIT_AUTO / confirm=$UNIT_CONFIRM / human=$UNIT_HUMAN"

# ---------------------------------------------------------------- 测试单元
step "准备:用户域测试单元(两个用户各自的 user 域)"
write_unit() { # write_unit <目录> <文件名>
  cat > "$1/$2" <<'EOF'
[Unit]
Description=Daedalus tx E2E 测试单元

[Service]
Type=simple
ExecStart=/usr/bin/sleep infinity

[Install]
WantedBy=default.target
EOF
}
UNIT_DIR="$HOME/.config/systemd/user"
mkdir -p "$UNIT_DIR"
for u in "$UNIT_AUTO" "$UNIT_CONFIRM" "$UNIT_HUMAN"; do
  write_unit "$UNIT_DIR" "$u"
done
systemctl --user daemon-reload
for u in "$UNIT_AUTO" "$UNIT_CONFIRM" "$UNIT_HUMAN"; do
  systemctl --user enable "$u" >/dev/null 2>&1
  systemctl --user stop "$u" >/dev/null 2>&1
done
systemctl --user list-unit-files "$UNIT_PREFIX*" --no-pager >> "$EV" 2>&1

# 第二用户同一套单元名:作用域只到单元名,不含用户名,所以两个 user 域里同名单元
# 走同一条档位规则 —— 这样才验得到「同一意图在两个用户下是两件事」。
SECOND_UNIT_DIR="$SECOND_HOME_DIR/.config/systemd/user"
sudo install -d -m 0755 -o "$SECOND_USER" -g "$SECOND_USER" "$SECOND_UNIT_DIR"
for u in "$UNIT_CONFIRM" "$UNIT_HUMAN"; do
  sudo tee "$SECOND_UNIT_DIR/$u" >/dev/null <<'EOF'
[Unit]
Description=Daedalus tx E2E 测试单元

[Service]
Type=simple
ExecStart=/usr/bin/sleep infinity

[Install]
WantedBy=default.target
EOF
  sudo chown "$SECOND_USER:$SECOND_USER" "$SECOND_UNIT_DIR/$u"
  sudo chmod 0644 "$SECOND_UNIT_DIR/$u"
done
as_second systemctl --user daemon-reload
for u in "$UNIT_CONFIRM" "$UNIT_HUMAN"; do
  as_second systemctl --user enable "$u" >/dev/null 2>&1
  as_second systemctl --user stop "$u" >/dev/null 2>&1
done
as_second systemctl --user list-unit-files "$UNIT_PREFIX*" --no-pager >> "$EV" 2>&1

# ---------------------------------------------------------------- 场景 1
step "场景 1(第一档 auto):预览 + 执行,单元应真的起来"
R1="$(mcp_call tx_preview "{\"kind\":\"service\",\"name\":\"$UNIT_AUTO\",\"desired_state\":\"started\"}")"
note "tx_preview → $(json_get "$R1" tier)"
check "场景1:preview 判为 auto" auto "$(json_get "$R1" tier)"
R1B="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_AUTO\",\"desired_state\":\"started\"}")"
note "tx_apply → $(json_get "$R1B" outcome)"
check "场景1:apply 判为 applied" applied "$(json_get "$R1B" outcome)"
if systemctl --user is-active --quiet "$UNIT_AUTO"; then
  check "场景1:单元真的在跑" active active
else
  check "场景1:单元真的在跑" active inactive
fi

# ---------------------------------------------------------------- 场景 2
step "场景 2(第二档 confirm):无条子被拒 → 补条子 → 放行"
R2="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_CONFIRM\",\"desired_state\":\"restarted\"}")"
say "R2 原始: $R2"
note "无条子 apply → error=$(json_get "$R2" error) required_scope=$(json_get "$R2" required_scope)"
check "场景2:无条子被拒" grant_required "$(json_get "$R2" error)"
if [ -n "$(json_get "$R2" required_scope)" ]; then
  check "场景2:给出所需范围" yes yes
else
  check "场景2:给出所需范围" yes no
fi
if systemctl --user is-active --quiet "$UNIT_CONFIRM"; then
  check "场景2:被拒后单元没动" untouched moved
else
  check "场景2:被拒后单元没动" untouched untouched
fi

# 条子由**签发者**签。两种形态都要验:
#   用户签(不 sudo):属主 = 用户,按该用户的 [[signer]] 上限判(所以上表必须有他的条目);
#   root 签(sudo -i):属主 = root(root 恒可签,不受 [[signer]] 约束)。
GRANT_RAW="$("$TX_BIN" grant add --scope "systemd:user:restart:$UNIT_CONFIRM" --ttl 10m --count 1 2>&1)"
GRANT_RC=$?
show_raw "$GRANT_RAW" "$GRANT_RC"
GRANT_ID="$(printf '%s' "$GRANT_RAW" | python3 -c "import json,sys;print(json.load(sys.stdin)['id'])" 2>/dev/null)"
note "条子 id = $GRANT_ID"
GRANT_FILE="$(ls -1 "$GRANTS_DIR"/*.json 2>/dev/null | head -1)"
if [ -n "$GRANT_FILE" ]; then
  note "条子文件: $GRANT_FILE"
  ls -l "$GRANT_FILE" >> "$EV"
  OWNER="$(stat -c %U "$GRANT_FILE")"
  check "条子属主=真实发起人" "$(id -un)" "$OWNER"
else
  check "条子签发成功" yes no
fi

# 2a: 系统路径还没有账本目录 ⇒ 账本会落到用户 HOME ⇒ 必须 fail-closed 拒绝
# 账本目录必须真的不存在,否则上一次跑留下的 1777 目录会让账本直接落在系统路径,
# 这条断言就永远绿、什么也没验到。
sudo rm -rf /var/lib/daedalus/tx
R2B="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_CONFIRM\",\"desired_state\":\"restarted\",\"grant_id\":\"$GRANT_ID\"}")"
say "R2B 原始: $R2B"
note "账本落 HOME 时 apply → error=$(json_get "$R2B" error)"
check "场景2a:账本落 HOME ⇒ 拒绝执行" grant_usage_failed "$(json_get "$R2B" error)"
if systemctl --user is-active --quiet "$UNIT_CONFIRM"; then
  check "场景2a:被拒后单元没动" untouched moved
else
  check "场景2a:被拒后单元没动" untouched untouched
fi

# 2b: 按 tmpfiles 建好系统路径账本目录 ⇒ 放行
sudo install -d -m 1777 -o root -g root /var/lib/daedalus/tx
R2C="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_CONFIRM\",\"desired_state\":\"restarted\",\"grant_id\":\"$GRANT_ID\"}")"
say "R2C 原始: $R2C"
note "账本在系统路径 apply → outcome=$(json_get "$R2C" outcome) grant_id=$(json_get "$R2C" grant_id)"
check "场景2b:有匹配条子放行" applied "$(json_get "$R2C" outcome)"

# root 签的条子:属主=root,但「给谁用」必须写明 —— 没写明的 root 条子谁都用不了。
ROOT_FOR_US_RAW="$(sudo env -u SUDO_USER "$TX_BIN" grant add --for "$(id -un)" \
  --scope "systemd:user:restart:$UNIT_CONFIRM" --ttl 10m --count 1 2>&1)"
ROOT_FOR_US_RC=$?
show_raw "$ROOT_FOR_US_RAW" "$ROOT_FOR_US_RC"
check "场景2c:root 签 --for 成功" 0 "$ROOT_FOR_US_RC"
ROOT_FOR_US_ID="$(printf '%s' "$ROOT_FOR_US_RAW" | python3 -c "import json,sys;print(json.load(sys.stdin)['id'])" 2>/dev/null)"
ROOT_GRANT="$GRANTS_DIR/$ROOT_FOR_US_ID.json"
if [ -f "$ROOT_GRANT" ]; then
  check "场景2c:root 签的条子属主=root" root "$(stat -c %U "$ROOT_GRANT")"
  check "场景2c:for_uid 指向本用户" "$(id -u)" "$(jq_file "$ROOT_GRANT" for_uid)"
else
  check "场景2c:root 签的条子属主=root" root none
  check "场景2c:for_uid 指向本用户" "$(id -u)" none
fi

# root 不写 --for:签出来的就是节点级通行证,必须当场拒(退出码 2)。
NOFOR_RC=0
run "root 不带 --for(应被拒)" sudo env -u SUDO_USER "$TX_BIN" grant add \
  --scope "systemd:user:restart:$UNIT_CONFIRM" --ttl 10m --count 1 || NOFOR_RC=$?
check "场景2c:root 不写 --for 被拒(退出码 2)" 2 "$NOFOR_RC"

# 给本用户签的那张 root 条子必须真能用:这是生产上最常见的签发形态。
R2Y="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_CONFIRM\",\"desired_state\":\"restarted\",\"grant_id\":\"$ROOT_FOR_US_ID\"}")"
say "R2Y 原始: $R2Y"
check "场景2c:root 给本用户签的条子放行" applied "$(json_get "$R2Y" outcome)"

# ---------------------------------------------------------------- 场景 2d
step "场景 2d(绑定):给别人签的条子本用户用不了 —— 真机越权的那条路径"
FOREIGN_ID=""
FOREIGN_RAW="$(sudo env -u SUDO_USER "$TX_BIN" grant add --for "$SECOND_USER" \
  --scope "systemd:user:restart:$UNIT_CONFIRM" --ttl 10m --count 1 2>&1)"
FOREIGN_RC=$?
show_raw "$FOREIGN_RAW" "$FOREIGN_RC"
FOREIGN_ID="$(printf '%s' "$FOREIGN_RAW" | python3 -c "import json,sys;print(json.load(sys.stdin)['id'])" 2>/dev/null)"
note "给 $SECOND_USER(uid=$SECOND_UID)签的条子 id = $FOREIGN_ID"
# 先把单元停下:它刚被 2c 重启过,不停下来就验不出「被拒之后没动」。
systemctl --user stop "$UNIT_CONFIRM" >/dev/null 2>&1
R2X="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_CONFIRM\",\"desired_state\":\"restarted\",\"grant_id\":\"$FOREIGN_ID\"}")"
say "R2X 原始: $R2X"
check "场景2d:借用别人的条子被拒" grant_for_other_user "$(json_get "$R2X" error)"
if systemctl --user is-active --quiet "$UNIT_CONFIRM"; then
  check "场景2d:被拒后单元没动" stopped moved
else
  check "场景2d:被拒后单元没动" stopped stopped
fi

# ---------------------------------------------------------------- 场景 2e
step "场景 2e(可发现):agent 能自己列出「手上能用哪些条子」"
G_SELF="$(mcp_call tx_grants '{}')"
say "tx_grants(本用户) 原始: $G_SELF"
check "场景2e:清单里的执行者=本用户" "$(id -u)" "$(json_get "$G_SELF" executor.uid)"
SELF_IDS="$(json_get "$G_SELF" grants)"
case "$SELF_IDS" in
  *"\"$ROOT_FOR_US_ID\""*) check "场景2e:本用户可用的条子在清单里" yes yes ;;
  *) check "场景2e:本用户可用的条子在清单里" yes no ;;
esac
if [ -n "$FOREIGN_ID" ]; then
  case "$SELF_IDS" in
    *"\"$FOREIGN_ID\""*) check "场景2e:别人的条子不出现在清单里" yes no ;;
    *) check "场景2e:别人的条子不出现在清单里" yes yes ;;
  esac
fi

# 每人一本账:第二个用户以自己身份问同一台服务器,看到的是他自己的账与绑定。
if [ -n "$FOREIGN_ID" ]; then
  G_OTHER="$(mcp_call_as "$SECOND_USER" tx_grants '{}')"
  say "tx_grants($SECOND_USER) 原始: $G_OTHER"
  check "场景2f:第二个用户的执行者身份=自己" "$SECOND_UID" "$(json_get "$G_OTHER" executor.uid)"
  OTHER_IDS="$(json_get "$G_OTHER" grants)"
  case "$OTHER_IDS" in
    *"\"$ROOT_FOR_US_ID\""*) check "场景2f:第二个用户看不到本用户的条子" yes no ;;
    *) check "场景2f:第二个用户看不到本用户的条子" yes yes ;;
  esac
  case "$OTHER_IDS" in
    *"\"$FOREIGN_ID\""*) check "场景2f:第二个用户能看到给他签的条子" yes yes ;;
    *) check "场景2f:第二个用户能看到给他签的条子" yes no ;;
  esac
fi

# ---------------------------------------------------------------- 场景 2g
step "场景 2g(正向绑定):root 给第二用户签条子,第二用户在自己的 user 域里执行"
SECOND_GRANT_RAW="$(sudo env -u SUDO_USER "$TX_BIN" grant add --for "$SECOND_USER" \
  --scope "systemd:user:restart:$UNIT_CONFIRM" --ttl 10m --count 1 2>&1)"
SECOND_GRANT_RC=$?
show_raw "$SECOND_GRANT_RAW" "$SECOND_GRANT_RC"
check "场景2g:root 给第二用户签条子成功" 0 "$SECOND_GRANT_RC"
SECOND_GRANT_ID="$(printf '%s' "$SECOND_GRANT_RAW" | python3 -c "import json,sys;print(json.load(sys.stdin)['id'])" 2>/dev/null)"
SECOND_GRANT_FILE="$GRANTS_DIR/$SECOND_GRANT_ID.json"
if [ -f "$SECOND_GRANT_FILE" ]; then
  ls -l "$SECOND_GRANT_FILE" >> "$EV"
  check "场景2g:条子 for_uid=第二用户 uid" "$SECOND_UID" "$(jq_file "$SECOND_GRANT_FILE" for_uid)"
  check "场景2g:条子属主=root" root "$(stat -c %U "$SECOND_GRANT_FILE")"
else
  check "场景2g:条子 for_uid=第二用户 uid" "$SECOND_UID" none
  check "场景2g:条子属主=root" root none
fi
if as_second systemctl --user is-active --quiet "$UNIT_CONFIRM"; then
  note "第二用户域里的 $UNIT_CONFIRM 已经是 active(不是本次执行造成)"
  SECOND_BEFORE=moved
else
  SECOND_BEFORE=inactive
fi
check "场景2g:执行前第二用户域里的单元没在跑" inactive "$SECOND_BEFORE"
R2G="$(mcp_call_as "$SECOND_USER" tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_CONFIRM\",\"desired_state\":\"restarted\",\"grant_id\":\"$SECOND_GRANT_ID\"}")"
say "tx_apply($SECOND_USER, 条子) 原始: $R2G"
note "第二用户用条子 apply → outcome=$(json_get "$R2G" outcome) grant_id=$(json_get "$R2G" grant_id)"
check "场景2g:第二用户用给他的条子执行" applied "$(json_get "$R2G" outcome)"
if as_second systemctl --user is-active --quiet "$UNIT_CONFIRM"; then
  check "场景2g:第二用户域里的单元真的起来了" active active
else
  check "场景2g:第二用户域里的单元真的起来了" active inactive
fi
# 同一张条子本用户拿不到:绑定判定先于用量判定,所以即使条子已被用掉,
# 本用户得到的仍是「不是给你的」而不是「已用尽」。
R2H="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_CONFIRM\",\"desired_state\":\"restarted\",\"grant_id\":\"$SECOND_GRANT_ID\"}")"
say "R2H 原始: $R2H"
check "场景2g:同一张条子本用户用不了" grant_for_other_user "$(json_get "$R2H" error)"

# ---------------------------------------------------------------- 场景 3
step "场景 3(第三档 human):出待办 → 无批准不执行 → 人批准 → 执行一次"
R3="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_HUMAN\",\"desired_state\":\"restarted\"}")"
say "R3 原始: $R3"
note "human 档 apply → error=$(json_get "$R3" error) tier=$(json_get "$R3" tier) pending_id=$(json_get "$R3" pending_id)"
check "场景3:判为需要人批准" human_approval_required "$(json_get "$R3" error)"
PENDING_ID="$(json_get "$R3" pending_id)"
if systemctl --user is-active --quiet "$UNIT_HUMAN"; then
  check "场景3:未批准不执行" untouched moved
else
  check "场景3:未批准不执行" untouched untouched
fi
PENDING_FILE="$PENDING_DIR/$(id -u)/$PENDING_ID.json"
if [ -n "$PENDING_ID" ] && [ -f "$PENDING_FILE" ]; then
  check "场景3:待办已落盘" yes yes
  PENDING_SHA="$(json_get "$R3" argv_sha256)"
  if [ -z "$PENDING_SHA" ]; then
    PENDING_SHA="$(python3 -c "import json,sys,hashlib;d=json.load(open(sys.argv[1]));print(hashlib.sha256(b'\x00'.join(a.encode() for a in d['argv'])).hexdigest())" "$PENDING_FILE")"
  fi
  note "待办 argv 指纹: $PENDING_SHA"
  run "人批准(带 --expect-sha256)" sudo -i "$TX_BIN" approve "$PENDING_ID" --expect-sha256 "$PENDING_SHA"
  if [ -f "$APPROVALS_DIR/$PENDING_ID.json" ]; then
    check "场景3:批准记录已落盘" yes yes
    ls -l "$APPROVALS_DIR/$PENDING_ID.json" >> "$EV"
  else
    check "场景3:批准记录已落盘" yes no
  fi
  # 批准后 argv 必须与被批准的完全一致:改成别的参数应当被拒。
  R3B="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_HUMAN\",\"desired_state\":\"restarted\"}")"
  note "批准后 apply → outcome=$(json_get "$R3B" outcome)"
  check "场景3:批准后执行" applied "$(json_get "$R3B" outcome)"
  # 批准是一次性的:同一份批准第二次使用必须被拒,而且要报「已经用过」,
  # 不能与「批的不是这条命令」混成一个码。
  CONSUMED_MARK="$CONSUMED_DIR/$(id -u)/$PENDING_ID"
  if [ -f "$CONSUMED_MARK" ]; then
    check "场景3:消费标记已落盘" yes yes
    ls -l "$CONSUMED_MARK" >> "$EV"
  else
    check "场景3:消费标记已落盘" yes no
  fi
  R3A="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_HUMAN\",\"desired_state\":\"restarted\"}")"
  say "R3A 原始: $R3A"
  note "同一份批准第二次使用 → error=$(json_get "$R3A" error)"
  check "场景3:同一份批准第二次使用被拒" approval_consumed "$(json_get "$R3A" error)"
else
  check "场景3:待办已落盘" yes no
fi

# ---------------------------------------------------------------- 场景 3b
step "场景 3b(待办带人):两个用户对同一意图不得撞同一个待办 id"
R3_OTHER="$(mcp_call_as "$SECOND_USER" tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_HUMAN\",\"desired_state\":\"restarted\"}")"
say "tx_apply($SECOND_USER, human 档) 原始: $R3_OTHER"
OTHER_PENDING="$(json_get "$R3_OTHER" pending_id)"
note "第二用户的待办 id = $OTHER_PENDING;本用户的待办 id = ${PENDING_ID:-}"
if [ -n "$OTHER_PENDING" ] && [ "$OTHER_PENDING" != "${PENDING_ID:-}" ]; then
  check "场景3b:两个用户的待办 id 不撞" yes yes
else
  check "场景3b:两个用户的待办 id 不撞" yes no
fi

# ---------------------------------------------------------------- 场景 3c
step "场景 3c(归属):待办按人分目录、批准记录带请求者、清单按归属显示"
OTHER_FILE="$PENDING_DIR/$SECOND_UID/$OTHER_PENDING.json"
# 第二用户的待办目录是 0700:调用者连 stat 里面的文件都会被拒,所以这条判定
# 必须由 root 做 —— 用调用者的身份探,探到的永远是"不存在"。
if sudo test -f "$OTHER_FILE"; then
  check "场景3c:第二用户待办落在自己的 uid 子目录" yes yes
  sudo ls -l "$OTHER_FILE" >> "$EV" 2>&1
else
  check "场景3c:第二用户待办落在自己的 uid 子目录" yes no
fi
if [ -f "$PENDING_FILE" ]; then
  check "场景3c:本用户待办落在自己的 uid 子目录" yes yes
  ls -l "$PENDING_FILE" >> "$EV"
else
  check "场景3c:本用户待办落在自己的 uid 子目录" yes no
fi
# 老布局是「待办直接躺在根目录」:根目录里还有 p-*.json 就说明分目录没生效。
if [ -n "${PENDING_ID:-}" ] && [ -f "$PENDING_DIR/$PENDING_ID.json" ]; then
  check "场景3c:待办不再落在待办根目录" absent present
else
  check "场景3c:待办不再落在待办根目录" absent absent
fi
# 子目录必须是 0700 且属主是本人:别人读不到,也抢建不了同名文件卡住落位。
check "场景3c:本用户待办目录 0700 且属主=本人" "700 $(id -un)" \
  "$(stat -c '%a %U' "$PENDING_DIR/$(id -u)" 2>/dev/null || echo missing)"
check "场景3c:第二用户待办目录 0700 且属主=本人" "700 $SECOND_USER" \
  "$(stat -c '%a %U' "$PENDING_DIR/$SECOND_UID" 2>/dev/null || echo missing)"

# root 的跨目录视图:审批的人要能一次看到所有用户提的请求,并知道每份是谁要的。
PL_RAW="$(sudo "$TX_BIN" pending list 2>&1)"
PL_RC=$?
show_raw "$PL_RAW" "$PL_RC"
check "场景3c:root pending list 退出码 0" 0 "$PL_RC"
PL_IDS="$(python3 - "$PL_RAW" <<'PY'
import json, sys
try:
    doc = json.loads(sys.argv[1])
except Exception:
    print(""); raise SystemExit(0)
print(" ".join(sorted(e.get("id", "") for e in doc.get("pending", []))))
PY
)"
note "pending list 里的 id: $PL_IDS"
case " $PL_IDS " in
  *" ${PENDING_ID:-<none>} "*) check "场景3c:清单里有本用户的待办" yes yes ;;
  *) check "场景3c:清单里有本用户的待办" yes no ;;
esac
case " $PL_IDS " in
  *" ${OTHER_PENDING:-<none>} "*) check "场景3c:清单里有第二用户的待办" yes yes ;;
  *) check "场景3c:清单里有第二用户的待办" yes no ;;
esac
check "场景3c:清单里本用户待办的 requested_by" "$(id -un)" "$(pl_field "$PL_RAW" "${PENDING_ID:-}" requested_by)"
check "场景3c:清单里本用户待办的 requested_by_uid" "$(id -u)" "$(pl_field "$PL_RAW" "${PENDING_ID:-}" requested_by_uid)"
check "场景3c:清单里第二用户待办的 requested_by" "$SECOND_USER" "$(pl_field "$PL_RAW" "${OTHER_PENDING:-}" requested_by)"
check "场景3c:清单里第二用户待办的 requested_by_uid" "$SECOND_UID" "$(pl_field "$PL_RAW" "${OTHER_PENDING:-}" requested_by_uid)"

# 批准第二用户那份待办:批准记录必须写明「批的是谁的请求」,权威是待办文件的属主。
OTHER_SHA="$(pl_field "$PL_RAW" "${OTHER_PENDING:-}" argv_sha256)"
run "人批准第二用户的待办" sudo -i "$TX_BIN" approve "$OTHER_PENDING" --expect-sha256 "$OTHER_SHA"
OTHER_APPROVAL="$APPROVALS_DIR/$OTHER_PENDING.json"
if [ -f "$OTHER_APPROVAL" ]; then
  ls -l "$OTHER_APPROVAL" >> "$EV"
  check "场景3c:批准记录 requested_by_uid=第二用户" "$SECOND_UID" "$(jq_file "$OTHER_APPROVAL" requested_by_uid)"
  check "场景3c:批准记录 requested_by=第二用户账号名" "$SECOND_USER" "$(jq_file "$OTHER_APPROVAL" requested_by)"
else
  check "场景3c:批准记录 requested_by_uid=第二用户" "$SECOND_UID" none
  check "场景3c:批准记录 requested_by=第二用户账号名" "$SECOND_USER" none
fi

# tx_pending 只列自己的:别人的待办在文件系统层就读不到(0700)。
TP_OTHER="$(mcp_call_as "$SECOND_USER" tx_pending '{}')"
say "tx_pending($SECOND_USER) 原始: $TP_OTHER"
TP_OTHER_ITEMS="$(json_get "$TP_OTHER" items)"
case "$TP_OTHER_ITEMS" in
  *"${OTHER_PENDING:-<none>}"*) check "场景3c:第二用户 tx_pending 里有自己的待办" yes yes ;;
  *) check "场景3c:第二用户 tx_pending 里有自己的待办" yes no ;;
esac
case "$TP_OTHER_ITEMS" in
  *"${PENDING_ID:-<none>}"*) check "场景3c:第二用户 tx_pending 不含本用户的待办" yes no ;;
  *) check "场景3c:第二用户 tx_pending 不含本用户的待办" yes yes ;;
esac
TP_SELF="$(mcp_call tx_pending '{}')"
say "tx_pending(本用户) 原始: $TP_SELF"
TP_SELF_ITEMS="$(json_get "$TP_SELF" items)"
case "$TP_SELF_ITEMS" in
  *"${PENDING_ID:-<none>}"*) check "场景3c:本用户 tx_pending 里有自己的待办" yes yes ;;
  *) check "场景3c:本用户 tx_pending 里有自己的待办" yes no ;;
esac
case "$TP_SELF_ITEMS" in
  *"${OTHER_PENDING:-<none>}"*) check "场景3c:本用户 tx_pending 不含第二用户的待办" yes no ;;
  *) check "场景3c:本用户 tx_pending 不含第二用户的待办" yes yes ;;
esac

# ---------------------------------------------------------------- 场景 3e
step "场景 3e(正向):第二用户用「批给他的」那份批准执行"
R3E="$(mcp_call_as "$SECOND_USER" tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_HUMAN\",\"desired_state\":\"restarted\"}")"
say "tx_apply($SECOND_USER, 已批准) 原始: $R3E"
note "第二用户已批准 apply → outcome=$(json_get "$R3E" outcome) approved_by=$(json_get "$R3E" approved_by)"
check "场景3e:第二用户用被批准的待办执行" applied "$(json_get "$R3E" outcome)"
if as_second systemctl --user is-active --quiet "$UNIT_HUMAN"; then
  check "场景3e:第二用户域里的单元真的动了" active active
else
  check "场景3e:第二用户域里的单元真的动了" active inactive
fi

# ---------------------------------------------------------------- 场景 3d
step "场景 3d(参数变了):人批准的是被改写过的 argv,agent 执行时指纹对不上"
# 待办文件属主能在人审查之前/之后把整份内容改写。人看的是 `pending show` 给出的
# argv 与指纹,所以这里让他批准**被改写过的**那一条:批准记录里的指纹从此对应
# 另一条命令,agent 按真实意图执行时必须撞指纹 ⇒ approval_mismatch,而不是执行。
R3D="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_HUMAN\",\"desired_state\":\"stopped\"}")"
P3_ID="$(json_get "$R3D" pending_id)"
P3_FILE="$PENDING_DIR/$(id -u)/$P3_ID.json"
say "tx_apply(stopped) 原始: $R3D"
note "新待办 id = $P3_ID 文件 = $P3_FILE"
check "场景3d:stop 也判为第三档" human "$(json_get "$R3D" tier)"
if [ -n "$P3_ID" ] && [ -f "$P3_FILE" ]; then
  P3_SHA="$(json_get "$R3D" argv_sha256)"
  cp "$P3_FILE" "$SECOND_STAGE/p3-original.json"
  chmod 0644 "$SECOND_STAGE/p3-original.json"
  python3 - "$P3_FILE" <<'PY'
import json, sys
path = sys.argv[1]
with open(path) as fh:
    doc = json.load(fh)
doc["argv"] = ["systemctl", "--user", "restart", "sshd-e2e-other.service"]
with open(path, "w") as fh:
    json.dump(doc, fh, ensure_ascii=False)
PY
  note "待办 argv 已改写为 $(python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['argv'])" "$P3_FILE")"
  P3_SHOW="$(sudo "$TX_BIN" pending show "$P3_ID" 2>&1)"
  show_raw "$P3_SHOW" $?
  TAMPERED_SHA="$(printf '%s' "$P3_SHOW" | python3 -c "import json,sys;print(json.load(sys.stdin).get('argv_sha256',''))" 2>/dev/null)"
  note "人看到的指纹 = $TAMPERED_SHA(原始指纹 = $P3_SHA)"
  run "人批准被改写的那一条" sudo -i "$TX_BIN" approve "$P3_ID" --expect-sha256 "$TAMPERED_SHA"
  R3DM="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_HUMAN\",\"desired_state\":\"stopped\"}")"
  say "agent 执行时 apply 原始: $R3DM"
  note "agent 执行时 apply → error=$(json_get "$R3DM" error)"
  check "场景3d:参数变了 ⇒ approval_mismatch" approval_mismatch "$(json_get "$R3DM" error)"
  if systemctl --user is-active --quiet "$UNIT_HUMAN"; then
    check "场景3d:指纹不符时不执行" active active
  else
    check "场景3d:指纹不符时不执行" active stopped
  fi
  # 复原成真实意图那份 argv,让人按真实指纹重批一次:同一份路径随后仍然可用,
  # 说明上面那条红是「批的不是这一条」,不是这条通道坏了。
  cp "$SECOND_STAGE/p3-original.json" "$P3_FILE"
  chmod 0644 "$P3_FILE"
  P3_SHOW_OK="$(sudo "$TX_BIN" pending show "$P3_ID" 2>&1)"
  P3_SHA_OK="$(printf '%s' "$P3_SHOW_OK" | python3 -c "import json,sys;print(json.load(sys.stdin).get('argv_sha256',''))" 2>/dev/null)"
  run "人按真实指纹重批" sudo -i "$TX_BIN" approve "$P3_ID" --expect-sha256 "$P3_SHA_OK"
  R3DO="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_HUMAN\",\"desired_state\":\"stopped\"}")"
  say "重批后 apply 原始: $R3DO"
  check "场景3d:按真实指纹重批后可用" applied "$(json_get "$R3DO" outcome)"
else
  check "场景3d:参数变了 ⇒ approval_mismatch" approval_mismatch none
fi

# ---------------------------------------------------------------- 场景 3f
step "场景 3f(伪造归属):落在受害者 id 上的待办被批准后,受害者本人不得执行"
# 待办 id 里带执行者 uid,但文件名是可以被人为写出来的:别人能在**自己的**目录里
# 放一份 id 等于受害者 id 的待办。门是「批准记录里的请求者」—— 批的是别人,
# 受害者本人执行时必须被拒,而不是因为指纹碰巧相同就放行。
if [ -n "${PENDING_ID:-}" ] && [ -f "$PENDING_DIR/$(id -u)/$PENDING_ID.json" ]; then
  cp "$PENDING_DIR/$(id -u)/$PENDING_ID.json" "$SECOND_STAGE/p1-forged.json"
fi
if [ -f "$SECOND_STAGE/p1-forged.json" ]; then
  chmod 0644 "$SECOND_STAGE/p1-forged.json"
  # 受害者目录里那份已消费掉,先移走,否则同一 id 出现在两个用户目录里,
  # root 的 approve 会按「重复即伪造」整份拒掉(那也是正确行为,但验不到本条)。
  rm -f "$PENDING_DIR/$(id -u)/$PENDING_ID.json"
  as_second cp "$SECOND_STAGE/p1-forged.json" "$PENDING_DIR/$SECOND_UID/$PENDING_ID.json"
  note "伪造件属主=$(sudo stat -c '%U' "$PENDING_DIR/$SECOND_UID/$PENDING_ID.json" 2>/dev/null || echo missing)"
  P1_SHOW="$(sudo "$TX_BIN" pending show "$PENDING_ID" 2>&1)"
  show_raw "$P1_SHOW" $?
  check "场景3f:root 看到的请求者是第二用户" "$SECOND_UID" \
    "$(printf '%s' "$P1_SHOW" | python3 -c "import json,sys;print(json.load(sys.stdin).get('requested_by_uid',''))" 2>/dev/null)"
  # 人按自己看到的指纹批准 —— 指纹来自 show 的输出,不是脚本里的旧变量。
  P1_SHA="$(printf '%s' "$P1_SHOW" | python3 -c "import json,sys;print(json.load(sys.stdin).get('argv_sha256',''))" 2>/dev/null)"
  run "人把这份伪造待办批了" sudo -i "$TX_BIN" approve "$PENDING_ID" --expect-sha256 "$P1_SHA"
  systemctl --user stop "$UNIT_HUMAN" >/dev/null 2>&1
  R3F="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_HUMAN\",\"desired_state\":\"restarted\"}")"
  say "受害者本人 apply 原始: $R3F"
  note "受害者本人 apply → error=$(json_get "$R3F" error)"
  check "场景3f:批给别人的批准受害者用不了" approval_for_other_user "$(json_get "$R3F" error)"
  if systemctl --user is-active --quiet "$UNIT_HUMAN"; then
    check "场景3f:被拒后单元没动" stopped moved
  else
    check "场景3f:被拒后单元没动" stopped stopped
  fi
else
  check "场景3f:批给别人的批准受害者用不了" approval_for_other_user none
fi

# ---------------------------------------------------------------- 场景 4
step "场景 4(危险名单):命中 policy.toml 的 high_risk_services ⇒ 至少第三档"
R4="$(mcp_call tx_preview "{\"kind\":\"service\",\"name\":\"$UNIT_DANGER\",\"desired_state\":\"started\"}")"
note "危险单元 preview → tier=$(json_get "$R4" tier)"
TIER_DANGER="$(json_get "$R4" tier)"
check "场景4:危险名单降档(至少第三档)" human "$TIER_DANGER"

# ---------------------------------------------------------------- 场景 5
step "场景 5(拒绝信息可区分):四类拒绝各给不同理由"
R5="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_DANGER\",\"desired_state\":\"stopped\"}")"
note "危险单元 apply → error=$(json_get "$R5" error)"
if [ -n "$(json_get "$R5" error)" ]; then check "场景5:拒绝带可区分码" yes yes; else check "场景5:拒绝带可区分码" yes no; fi

# ---------------------------------------------------------------- 场景 6
step "场景 6(每人一本账):两个用户各有一本用量账本"
LEDGER_SELF="/var/lib/daedalus/tx/usage-$(id -u).jsonl"
LEDGER_OTHER="/var/lib/daedalus/tx/usage-$SECOND_UID.jsonl"
if [ -f "$LEDGER_SELF" ]; then
  check "场景6:本用户的账本存在" yes yes
  ls -l "$LEDGER_SELF" >> "$EV"
  check "场景6:本用户账本属主=本人" "$(id -un)" "$(stat -c %U "$LEDGER_SELF")"
else
  check "场景6:本用户的账本存在" yes no
fi
if [ -f "$LEDGER_OTHER" ]; then
  check "场景6:第二用户的账本存在" yes yes
  ls -l "$LEDGER_OTHER" >> "$EV"
  check "场景6:第二用户账本属主=第二用户" "$SECOND_USER" "$(stat -c %U "$LEDGER_OTHER")"
  # 账本是 0600 的私人文件:读它要 root(或本人)。用调用者身份读会得到 EACCES,
  # 那会把"账本记着谁的条子"这件事验成一条无意义的红。
  check "场景6:第二用户账本记的是他自己的条子" yes \
    "$(sudo python3 - "$LEDGER_OTHER" "$SECOND_GRANT_ID" <<'PY'
import json, sys
path, gid = sys.argv[1], sys.argv[2]
hit = False
for line in open(path, encoding="utf-8"):
    line = line.strip()
    if not line:
        continue
    try:
        hit = hit or json.loads(line).get("grant_id") == gid
    except Exception:
        continue
print("yes" if hit else "no")
PY
)"
else
  check "场景6:第二用户的账本存在" yes no
fi
# 两个用户走第二档时用的不是同一个文件:名字按 uid 分,谁的账本归谁。
if [ -f "$LEDGER_SELF" ] && [ -f "$LEDGER_OTHER" ]; then
  if [ "$LEDGER_SELF" = "$LEDGER_OTHER" ]; then
    check "场景6:两本账本不是同一个文件" two one
  else
    check "场景6:两本账本不是同一个文件" two two
  fi
fi

# ---------------------------------------------------------------- 清理
step "清理"
systemctl --user disable --now "$UNIT_AUTO" "$UNIT_CONFIRM" "$UNIT_HUMAN" >/dev/null 2>&1
rm -f "$UNIT_DIR/$UNIT_AUTO" "$UNIT_DIR/$UNIT_CONFIRM" "$UNIT_DIR/$UNIT_HUMAN"
systemctl --user daemon-reload
sudo rm -f "$AUTONOMY_PATH" 2>/dev/null || true
cleanup_second_user
rm -rf "${SECOND_STAGE:-}" 2>/dev/null || true

finish
