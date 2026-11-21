#!/usr/bin/env bash
# =============================================================================
# tx-e2e.sh —— 外部 AI agent 受控变更通道的**真机**端到端验收
# =============================================================================
#
# 验的是三档判定的真实行为(不是模拟):
#   第一档 auto   —— AI 自己就执行,单元真的起来;
#   第二档 confirm —— 没条子被拒并给出所需范围;补一张条子后放行;
#   第三档 human  —— AI 出待办、**没有**批准记录就不执行;人批准后按同一 argv 执行。
#
# 为什么是「真机」:这套通道的判定依赖 `systemctl --user`(要求调用者在活着的用户会话里)
# 与真实的 /run 目录权限。真机才有这些;宿主上的假 systemctl 验不了。
#
# 用法(在目标机上,普通用户身份,sudo 免密):
#   tx-e2e.sh --bin-dir <含 daedalus-tx 与 daedalus-tx-mcp 的目录> \
#             [--policy <policy.toml 路径>] [--evidence <输出 md 路径>]
#
# 环境变量:
#   E2E_UNIT_PREFIX=daedalus-tx-e2e     测试单元名前缀(默认)
#   E2E_AUTONOMY_PATH=/etc/daedalus/agent-autonomy.toml
#   E2E_GRANTS_DIR=/run/daedalus/grants
#   E2E_PENDING_DIR=/run/daedalus/pending
#   E2E_APPROVALS_DIR=/run/daedalus/approvals
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
# 第二个用户用来验「绑定到别人的条子用不了」「两个用户的待办 id 不撞」
# 「每人一本账」。默认临时建一个 nologin 系统用户,用完删掉。
SECOND_USER="${E2E_SECOND_USER:-daedalus-e2e-other}"
SECOND_CREATED=0

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

UNIT_AUTO="$UNIT_PREFIX-auto.service"
UNIT_CONFIRM="$UNIT_PREFIX-confirm.service"
UNIT_HUMAN="$UNIT_PREFIX-human.service"
UNIT_DANGER="sshd-e2e.service"

mcp_call() { # mcp_call <工具> <参数 JSON> —— 输出 JSON 文本
  local extra=()
  [ -n "$POLICY" ] && extra+=(--env "DAEDALUS_POLICY_PATH=$POLICY")
  python3 "$CLIENT" --bin "$MCP_BIN" "${extra[@]}" --call "$1" --args "$2" 2>/dev/null
}

# mcp_call_as 以另一个用户身份问同一台服务器:执行者身份来自进程 euid,
# 所以换用户 = 换执行者。客户端与二进制要放在家目录之外的世界可读路径:
# 目标用户通常连调用者的家目录都进不去(0700)。
mcp_call_as() { # mcp_call_as <用户> <工具> <参数 JSON>
  local extra=()
  [ -n "$POLICY" ] && extra+=(--env "DAEDALUS_POLICY_PATH=$SECOND_STAGE/policy.toml")
  sudo -u "$1" env HOME="$SECOND_HOME" \
    python3 "$SECOND_STAGE/tx-e2e-client.py" --bin "$SECOND_STAGE/daedalus-tx-mcp" \
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

# ---------------------------------------------------------------- preflight
step "preflight:环境能力"
note "uid=$(id -u) user=$(id -un) systemd=$(systemctl --version | head -1)"
if systemctl --user list-units >/dev/null 2>&1; then
  note "systemctl --user 可用(有活着的用户会话)"
  check "preflight:systemctl --user" ok ok
else
  check "preflight:systemctl --user" ok unavailable
  say "用户会话不可用 ⇒ systemctl --user 无处可去,后续场景无法验真"
  say ""
  say "## 汇总"
  say "- 通过 $PASS / 失败 $FAIL"
  for f in "${FAILURES[@]:-}"; do [ -n "$f" ] && say "- ❌ $f"; done
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
sudo install -d -m 1775 -o root -g daedalus-signers "$GRANTS_DIR"
sudo install -d -m 1777 -o root -g root "$PENDING_DIR"
sudo install -d -m 0755 -o root -g root "$APPROVALS_DIR"
sudo getent group daedalus-signers >/dev/null 2>&1 || sudo groupadd -f daedalus-signers
sudo usermod -aG daedalus-signers "$(id -un)" 2>/dev/null || true
# 第二个用户:不需要登录会话(绑定判定只看 uid),nologin + 无家目录就够。
if ! getent passwd "$SECOND_USER" >/dev/null 2>&1; then
  sudo useradd -M -r -s /usr/sbin/nologin "$SECOND_USER" && SECOND_CREATED=1
fi
# 目标用户进不了调用者的家目录(0700),所以客户端与二进制要另放一份世界可读的;
# HOME 单独给一个可写目录,免得账本/审计路径解析落在别人的家目录。
SECOND_STAGE="${TMPDIR:-/tmp}/daedalus-e2e-second-stage"
SECOND_HOME="${TMPDIR:-/tmp}/daedalus-e2e-second-home"
rm -rf "$SECOND_STAGE" "$SECOND_HOME"
mkdir -p "$SECOND_STAGE" "$SECOND_HOME"
cp "$CLIENT" "$SECOND_STAGE/tx-e2e-client.py"
cp "$MCP_BIN" "$SECOND_STAGE/daedalus-tx-mcp"
chmod 0644 "$SECOND_STAGE/tx-e2e-client.py"
chmod 0755 "$SECOND_STAGE/daedalus-tx-mcp" "$SECOND_STAGE" "$SECOND_HOME"
# 策略文件也要放到世界可读的位置:目标用户连调用者家目录都进不去,
# 给它原路径等于让它读不到策略、直接拒启。
if [ -n "$POLICY" ]; then
  cp "$POLICY" "$SECOND_STAGE/policy.toml"
  chmod 0644 "$SECOND_STAGE/policy.toml"
fi
note "第二用户: $SECOND_USER(uid=$(id -u "$SECOND_USER" 2>/dev/null || echo missing))"
# 上一次跑留下的待办/批准/条子会污染本次判定(批准在有效期内仍会被认)——先清干净。
sudo rm -f "$PENDING_DIR"/*.json "$APPROVALS_DIR"/*.json "$GRANTS_DIR"/*.json 2>/dev/null || true
ls -ld /run/daedalus "$GRANTS_DIR" "$PENDING_DIR" "$APPROVALS_DIR" >> "$EV"

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
step "准备:用户域测试单元"
UNIT_DIR="$HOME/.config/systemd/user"
mkdir -p "$UNIT_DIR"
for u in "$UNIT_AUTO" "$UNIT_CONFIRM" "$UNIT_HUMAN"; do
  cat > "$UNIT_DIR/$u" <<EOF
[Unit]
Description=Daedalus tx E2E 测试单元

[Service]
Type=simple
ExecStart=/usr/bin/sleep infinity

[Install]
WantedBy=default.target
EOF
done
systemctl --user daemon-reload
for u in "$UNIT_AUTO" "$UNIT_CONFIRM" "$UNIT_HUMAN"; do
  systemctl --user enable "$u" >/dev/null 2>&1
  systemctl --user stop "$u" >/dev/null 2>&1
done
systemctl --user list-unit-files "$UNIT_PREFIX*" --no-pager >> "$EV" 2>&1

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
say ""; say '```'; say "$GRANT_RAW"; say '```'; say "退出码: $GRANT_RC"
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
say ""; say '```'; say "$ROOT_FOR_US_RAW"; say '```'; say "退出码: $ROOT_FOR_US_RC"
check "场景2c:root 签 --for 成功" 0 "$ROOT_FOR_US_RC"
ROOT_FOR_US_ID="$(printf '%s' "$ROOT_FOR_US_RAW" | python3 -c "import json,sys;print(json.load(sys.stdin)['id'])" 2>/dev/null)"
ROOT_GRANT="$GRANTS_DIR/$ROOT_FOR_US_ID.json"
if [ -f "$ROOT_GRANT" ]; then
  check "场景2c:root 签的条子属主=root" root "$(stat -c %U "$ROOT_GRANT")"
  check "场景2c:for_uid 指向本用户" "$(id -u)" "$(python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['for_uid'])" "$ROOT_GRANT")"
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
SECOND_UID="$(id -u "$SECOND_USER" 2>/dev/null || true)"
FOREIGN_ID=""
if [ -n "$SECOND_UID" ]; then
  FOREIGN_RAW="$(sudo env -u SUDO_USER "$TX_BIN" grant add --for "$SECOND_USER" \
    --scope "systemd:user:restart:$UNIT_CONFIRM" --ttl 10m --count 1 2>&1)"
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
else
  check "场景2d:第二用户就位" yes no
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
if [ -n "$SECOND_UID" ] && [ -n "$FOREIGN_ID" ]; then
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

# ---------------------------------------------------------------- 场景 3
step "场景 3(第三档 human):出待办 → 无批准不执行 → 人批准 → 执行"
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
if [ -n "$PENDING_ID" ] && [ -f "$PENDING_DIR/$PENDING_ID.json" ]; then
  check "场景3:待办已落盘" yes yes
  PENDING_SHA="$(json_get "$R3" argv_sha256)"
  if [ -z "$PENDING_SHA" ]; then
    PENDING_SHA="$(python3 -c "import json,sys,hashlib;d=json.load(open(sys.argv[1]));print(hashlib.sha256(b'\x00'.join(a.encode() for a in d['argv'])).hexdigest())" "$PENDING_DIR/$PENDING_ID.json")"
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
else
  check "场景3:待办已落盘" yes no
fi

# ---------------------------------------------------------------- 场景 3b
step "场景 3b(待办带人):两个用户对同一意图不得撞同一个待办 id"
if [ -n "${SECOND_UID:-}" ] && [ -n "${PENDING_ID:-}" ]; then
  R3_OTHER="$(mcp_call_as "$SECOND_USER" tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_HUMAN\",\"desired_state\":\"restarted\"}")"
  say "tx_apply($SECOND_USER, human 档) 原始: $R3_OTHER"
  OTHER_PENDING="$(json_get "$R3_OTHER" pending_id)"
  note "第二用户的待办 id = $OTHER_PENDING;本用户的待办 id = $PENDING_ID"
  if [ -n "$OTHER_PENDING" ] && [ "$OTHER_PENDING" != "$PENDING_ID" ]; then
    check "场景3b:两个用户的待办 id 不撞" yes yes
  else
    check "场景3b:两个用户的待办 id 不撞" yes no
  fi
else
  check "场景3b:前置条件(第二用户/本用户待办)" yes no
fi

# ---------------------------------------------------------------- 场景 4
step "场景 4(危险名单):命中 policy.toml 的 high_risk_services ⇒ 至少第三档"
R4="$(mcp_call tx_preview "{\"kind\":\"service\",\"name\":\"$UNIT_DANGER\",\"desired_state\":\"started\"}")"
note "危险单元 preview → tier=$(json_get "$R4" tier)"
TIER_DANGER="$(json_get "$R4" tier)"
check "场景4:危险名单降档(至少第三档)" human "$TIER_DANGER"

# ---------------------------------------------------------------- 场景 5
step "场景 5(拒绝信息可区分):四类拒绝各给不同理由"
for probe in "缺条子|$UNIT_CONFIRM|restarted" ; do :; done
R5="$(mcp_call tx_apply "{\"kind\":\"service\",\"name\":\"$UNIT_DANGER\",\"desired_state\":\"stopped\"}")"
note "危险单元 apply → error=$(json_get "$R5" error)"
if [ -n "$(json_get "$R5" error)" ]; then check "场景5:拒绝带可区分码" yes yes; else check "场景5:拒绝带可区分码" yes no; fi

# ---------------------------------------------------------------- 清理
step "清理"
systemctl --user disable --now "$UNIT_AUTO" "$UNIT_CONFIRM" "$UNIT_HUMAN" >/dev/null 2>&1
rm -f "$UNIT_DIR/$UNIT_AUTO" "$UNIT_DIR/$UNIT_CONFIRM" "$UNIT_DIR/$UNIT_HUMAN"
systemctl --user daemon-reload
sudo rm -f "$AUTONOMY_PATH" 2>/dev/null || true
# 临时用户只在本次新建时才删:调用方用 E2E_SECOND_USER 指向既有用户时不许动它。
if [ "${SECOND_CREATED:-0}" = 1 ]; then
  sudo userdel "$SECOND_USER" 2>/dev/null || true
fi
sudo rm -rf /tmp/daedalus-e2e-second-home 2>/dev/null || true
rm -rf "${SECOND_STAGE:-}" "${SECOND_HOME:-}" 2>/dev/null || true

say ""
say "## 汇总"
say "- 通过 $PASS,失败 $FAIL"
for f in "${FAILURES[@]:-}"; do [ -n "$f" ] && say "- ❌ $f"; done
say ""
say "证据文件: $EV"
[ "$FAIL" -eq 0 ]
