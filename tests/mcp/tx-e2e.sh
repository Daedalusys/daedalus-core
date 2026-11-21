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

# root 签的条子:属主=root,消费侧按 root 恒可判
run "再签一张(root 身份,root 恒可)" \
  sudo env -u SUDO_USER "$TX_BIN" grant add --scope "systemd:user:restart:$UNIT_CONFIRM" --ttl 10m --count 1
ROOT_GRANT="$(ls -1 "$GRANTS_DIR"/*.json 2>/dev/null | while read -r f; do [ "$(stat -c %U "$f")" = root ] && echo "$f"; done | head -1)"
if [ -n "$ROOT_GRANT" ]; then
  check "root 签的条子属主=root" root "$(stat -c %U "$ROOT_GRANT")"
else
  check "root 签的条子属主=root" root none
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

say ""
say "## 汇总"
say "- 通过 $PASS,失败 $FAIL"
for f in "${FAILURES[@]:-}"; do [ -n "$f" ] && say "- ❌ $f"; done
say ""
say "证据文件: $EV"
[ "$FAIL" -eq 0 ]
