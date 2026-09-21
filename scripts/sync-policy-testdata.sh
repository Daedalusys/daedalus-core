#!/usr/bin/env bash
# 同步生产 policy.toml 到 SDK 测试夹具(testdata/policy.toml)。
#
# 背景:SDK 的 3 点漂移测试只校验 testdata ↔ policy.Default() ↔ objectmodel
# 常量,若 testdata 与生产策略漂移会漏检——本脚本保证 testdata 恒为
# 生产 policy.toml 的完整一致副本(不是缩水 fixture)。
#
# 用法:从仓库根执行(CI 与本地 dev 均适用)。
set -euo pipefail

SRC="daedalus/files/system/opt/daedalus/shared/policy.toml"
DST="daedalus-sdk/policy/testdata/policy.toml"

[ -f "$SRC" ] || { echo "错误:生产策略不存在: $SRC(请从仓库根执行)" >&2; exit 1; }
cp "$SRC" "$DST"
echo "已同步: $SRC -> $DST"