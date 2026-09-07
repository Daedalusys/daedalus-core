#!/usr/bin/env bash
# i18n 键集交叉校验(plan aios-object-model-alignment todo 30 验收项):
#   (i)  en_US 与 zh_CN 键集必须完全一致(jq keys 排序 diff 为空);
#   (ii) main.ts / policy.ts 里每一个 t("<key>") 字面量,双 locale 文件都必须存在该键。
# 说明:just i18n-sync 只校验 manifest 声明 ↔ locale 实物与 Go 侧 t() 键,
#       不覆盖 TS 侧 t() 字面量与 en↔zh 键集对称——本脚本补上这道 CI 门禁。
# 正则纪律:lookbehind 排除 get(/split( 等尾缀 t 误报(T29 实测 3 类假阳性),
#       与 tests/deno/tx_aggregate.test.ts 的 TS 版正则保持同源。
set -euo pipefail

# 以脚本自身位置解析仓库根,任意 cwd 可运行
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
EN="$ROOT/daedalus/plugin/copilot/i18n/en_US.json"
ZH="$ROOT/daedalus/plugin/copilot/i18n/zh_CN.json"
SRC_MAIN="$ROOT/daedalus/plugin/copilot/main.ts"
SRC_POLICY="$ROOT/daedalus/plugin/copilot/policy.ts"

rc=0

# ---- (i) en ↔ zh 键集对称 ----------------------------------------------------
if ! diff <(jq -r 'keys[]' "$EN" | sort) <(jq -r 'keys[]' "$ZH" | sort); then
  echo "FAIL: en_US.json 与 zh_CN.json 键集不一致(上方 diff)" >&2
  rc=1
fi

# ---- (ii) t() 字面量双 locale 存在性 -----------------------------------------
# 提取全部 t("key") 字面量键(grep -P 支持 \K 与 lookbehind)
keys=$(grep -hoPr '(?<![\w$])t\(\s*"\K[^"]+' "$SRC_MAIN" "$SRC_POLICY" | sort -u)

while IFS= read -r key; do
  [ -n "$key" ] || continue
  # jq 精确取键;// empty 使缺键时输出为空而非 null
  if [ -z "$(jq -r --arg k "$key" '.[$k] // empty' "$EN")" ]; then
    echo "missing key: $key in en_US.json" >&2
    rc=1
  fi
  if [ -z "$(jq -r --arg k "$key" '.[$k] // empty' "$ZH")" ]; then
    echo "missing key: $key in zh_CN.json" >&2
    rc=1
  fi
done <<< "$keys"

# ---- 汇总 ---------------------------------------------------------------------
n=$(echo "$keys" | grep -c . || true)
if [ "$rc" -eq 0 ]; then
  echo "OK: $(jq -r 'keys | length' "$EN") 键 en↔zh 对称;$n 个 t() 字面量双 locale 全存在"
fi
exit "$rc"
