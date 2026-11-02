#!/usr/bin/env bash
# systemd 单元静态门禁:单元/drop-in 的**解析级**错误即拒。
#
# 解析级 = systemd 认不出键名或值,例如:
#   - SystemCallFilter 的取反前缀放在列表中间 → "Failed to parse system call, ignoring"
#   - 键写错节(如 ConditionPathExists 写进 [Service]) → "Unknown key name ... in section"
#
# 故意不检 "Command ... is not executable":源码树里插件二进制本就不存在,
# CI 上该告警对每个单元恒真、零信号。ExecStart 路径/旗标的正确性由构建期
# 76 脚本(render-unit 自校验)与 cmd/daedalus-host 的单测各自覆盖。
set -euo pipefail

command -v systemd-analyze >/dev/null || {
    echo "systemd-units: systemd-analyze 不可用,无法执行门禁" >&2
    exit 1
}

unit_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../files/system/usr/lib/systemd/system" && pwd)"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
cp -a "$unit_dir/." "$scratch/"

# systemd-analyze verify 会顺带解析宿主单元(依赖拉取),故只留提到本批单元的
# 告警行 —— 本批目录名统一带 "daedalus-" 前缀,宿主单元不会。
output="$(cd "$scratch" && systemd-analyze verify ./*.service ./*.timer 2>&1 || true)"
bad="$(printf '%s\n' "$output" \
    | grep -E 'Failed to parse|Unknown key name' \
    | grep -F 'daedalus-' || true)"

if [ -n "$bad" ]; then
    echo "systemd-units: 发现单元解析级错误:" >&2
    printf '%s\n' "$bad" >&2
    exit 1
fi
echo "OK: systemd 单元无解析级错误"
