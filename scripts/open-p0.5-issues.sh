#!/usr/bin/env bash
# T5 Step 1 — 批开 P0.5 新 issue(per spec §5.2)
set -euo pipefail

OPEN() { gh issue create --repo "$1" --title "$2" --body "$3" "${@:4}"; }

OPEN Daedalusys/daedalus-core "[P0.5] headless 变体裁剪" \
"**Spec**: docs/superpowers/specs/2026-10-17-fleet-control-plane-design.md §4.5 / G5 / A6

**范围**: Containerfile 改 / 79 删除 / 77 mask / 80 调整 / headless 标记
**依赖**: 无
**落点**: Plan A T1-T3
" --label "area:core,help wanted"

OPEN Daedalusys/daedalus-core "[P0.5] daedalus-agent 二进制 + daemon" \
"**Spec**: spec §4.2 / §5.2 core#38

**范围**: cmd/daedalus-agent/ Go 单二进制 / daemon start-stop / config file
**依赖**: Plan B(待写)
**落点**: Plan B T1-T2
" --label "area:core"

OPEN Daedalusys/daedalus-core "[P0.5] 控制面 ↔ agent RPC 协议" \
"**Spec**: spec §4.4 / §5.2 core#39

**范围**: mTLS / gRPC / protobuf schema / cert 续签
**依赖**: Plan B
**落点**: Plan B T3-T5
" --label "area:core"

OPEN Daedalusys/daedalus-core "[P0.5] Plan 类型 + 调度器" \
"**Spec**: spec §4.3 / §5.2 core#40

**范围**: daedalus-tx plan create/apply/status / 失败分桶 / 暂停语义 / risk_class
**依赖**: Plan C(待写)
**落点**: Plan C T1-T3
" --label "area:core"

OPEN Daedalusys/daedalus-core "[P0.5] daedalus-agent 异构被控 E2E" \
"**Spec**: spec §5.2 core#41 (对应 core#36 改 spec 后的子任务)

**范围**: Ubuntu / CentOS / Debian 三种宿主端到端
**依赖**: Plan B + C 完成
**落点**: Plan C 末
" --label "area:core,help wanted"

OPEN Daedalusys/daedalus-sdk "[P0.5] daedalus/agent/v1.proto + Go SDK" \
"**Spec**: spec §5.2 sdk#26

**范围**: wire schema (Register / ApplyIntent / ReportStatus / SnapshotRequest / Heartbeat)
**依赖**: Plan B
**落点**: Plan B T3
" --label "area:sdk"

OPEN Daedalusys/daedalus-sdk "[P0.5] Plan objectmodel 类型" \
"**Spec**: spec §5.2 sdk#27

**范围**: intents / machines / batching / blast_radius / risk_class 字段
**依赖**: Plan C
**落点**: Plan C T1
" --label "area:sdk"

OPEN Daedalusys/daedalus-sdk "[P0.5] SecretProvider 下发契约" \
"**Spec**: spec §5.2 sdk#28

**范围**: 不落 agent 的凭证下发流程 / 内存生命周期(C2)
**依赖**: Plan B
**落点**: Plan B 末
" --label "area:sdk"

OPEN Daedalusys/daedalus-plugins "[P0.5] daedalus.os-state 插件" \
"**Spec**: spec §5.2 plugins#34 (替代 plugins#8 扩 scope)

**范围**: 异构被控 OS 状态(apt / dnf / generic pkg mgr;FS type detection;snapshot capability)
**依赖**: Plan B
**落点**: Plan B 中
" --label "area:plugins"

OPEN Daedalusys/daedalus-plugins "[P0.5] daedalus.snapshot 插件" \
"**Spec**: spec §5.2 plugins#35

**范围**: btrfs / LVM / snapper / rsync 能力检测与调度
**依赖**: Plan B
**落点**: Plan B 中
" --label "area:plugins"

# 关闭 plugins#6 的"阻塞"注解
gh issue edit 6 --repo Daedalusys/daedalus-plugins --add-label "blocked-by:daedalus-sdk#2" 2>/dev/null || true

# 关 sdk#1 (P3 Object Model 已完成,见 spec §5.1)
gh issue close 1 --repo Daedalusys/daedalus-sdk --comment "P3 Object Model 已交付(2026-10-10 PR);按 spec §5.1 关闭。相关消费者(controller reconcile / finalizer GC)归 P4,见 daedalus-core#2。" 2>/dev/null || true

# 改 spec#36 / plugins#8 的描述(原 KDE 验法 / 含被控 scope)
gh issue edit 36 --repo Daedalusys/daedalus-core --body "按 spec §5.1,scope 缩到 daedalus-atomic headless 验证;异构被控 E2E 是新 issue (本批 [P0.5] daedalus-agent 异构被控 E2E)。原 KDE variant 验证已废(2026-10-17 fleet 改造)。" 2>/dev/null || true

gh issue edit 8 --repo Daedalusys/daedalus-plugins --body "按 spec §5.1,scope 只管 daedalus 本体的 bootc 状态;异构被控 OS 状态归 [P0.5] daedalus.os-state 插件(同批新开)。" 2>/dev/null || true

echo "==> issues created / edited; verify at:"
echo "  https://github.com/Daedalusys/daedalus-core/issues"
echo "  https://github.com/Daedalusys/daedalus-sdk/issues"
echo "  https://github.com/Daedalusys/daedalus-plugins/issues"
