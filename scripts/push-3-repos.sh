#!/usr/bin/env bash
# 三仓拆分推送脚本(V3 构建机用;本机无 gh,review Issue H 降级产物)。
#
# 背景:todo 12 物理改名已完成(daedalus/{core,plugin,files} → daedalus-core/),
# 主仓根布局 = daedalus-core/ + daedalus-sdk/ + daedalus-plugins/ + 仓根文件。
# 本脚本在 V3 构建机(有 gh + 网络)上执行:
#   1) gh repo create 创建 3 个公开仓
#   2) 用 git subtree split 把 daedalus-sdk/ 与 daedalus-plugins/ 拆成独立仓(保留历史)
#   3) 主仓整体改名 daedalus-core(原 Daedalusys/Daedalusys),直接推
#   4) 3 仓 main 分支保护(要求 PR + 1 review + CI pass)
#
# 用法:在仓库根执行 `bash scripts/push-3-repos.sh`(V3 构建机)。
# 前置:gh 已登录(`gh auth status`)、git 已配置 SSH key、仓库工作区干净或已提交。
set -euo pipefail

# ============ 0. 前置检查 ============
command -v gh >/dev/null 2>&1 || { echo "错误:本机无 gh,请先在 V3 构建机安装并登录" >&2; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "错误:gh 未登录,请先 gh auth login" >&2; exit 1; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# 布局自检:三个源树必须都在
for d in daedalus-core daedalus-sdk daedalus-plugins; do
    [ -d "$d" ] || { echo "错误:缺少 $d/,请确认 todo 12 物理改名已完成" >&2; exit 1; }
done

# ============ 1. 创建 3 个 GitHub 仓 ============
echo "=== 1/4 创建 GitHub 仓 ==="
# 已存在的仓跳过创建(gh repo create 对已存在仓报错,set -e 会中止脚本)
gh repo view Daedalusys/daedalus-core >/dev/null 2>&1 \
    || gh repo create Daedalusys/daedalus-core --public \
        --description "Daedalus image build + 5 core runtime (host/audit/tx/smoke/plugin-pack) + copilot"
gh repo view Daedalusys/daedalus-sdk >/dev/null 2>&1 \
    || gh repo create Daedalusys/daedalus-sdk --public \
        --description "Daedalus SDK: 11 security core packages (audit/policy/pathguard/shellpolicy/version/plugin/objectmodel/i18n/pkgquery/sysinfo/blueprint) + 5 Provider/Slot placeholders"
gh repo view Daedalusys/daedalus-plugins >/dev/null 2>&1 \
    || gh repo create Daedalusys/daedalus-plugins --public \
        --description "Daedalus plugins monorepo: 6 Go capability plugins (fs/shell/pkg/sysinfo/service/blueprint)"

# ============ 2. daedalus-core:主仓整体改名,直接推 ============
echo "=== 2/4 推送 daedalus-core(主仓) ==="
# 主仓 remote 从 Daedalusys/Daedalusys 改为 Daedalusys/daedalus-core
git remote set-url origin git@github.com:Daedalusys/daedalus-core.git
git push -u origin main

# ============ 3. daedalus-sdk / daedalus-plugins:subtree split 拆仓 ============
echo "=== 3/4 拆分并推送 daedalus-sdk / daedalus-plugins ==="
# 用 git subtree split 保留各自子树历史(拆出的分支内容在仓根,无嵌套目录)
git subtree split -P daedalus-sdk -b split-sdk
git subtree split -P daedalus-plugins -b split-plugins

# 临时目录里分别 init 两个新仓并推
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- daedalus-sdk ---
git clone -b split-sdk . "$TMP/sdk" 2>/dev/null || {
    # 若 clone 失败(本地分支不可 clone),退化为 worktree 方式
    git worktree add "$TMP/sdk" split-sdk
}
(
    cd "$TMP/sdk"
    git remote add origin git@github.com:Daedalusys/daedalus-sdk.git
    git push -u origin split-sdk:main
)

# --- daedalus-plugins ---
git clone -b split-plugins . "$TMP/plugins" 2>/dev/null || {
    git worktree add "$TMP/plugins" split-plugins
}
(
    cd "$TMP/plugins"
    git remote add origin git@github.com:Daedalusys/daedalus-plugins.git
    git push -u origin split-plugins:main
)

# 清理临时 split 分支(保留本地分支亦可,删除避免污染主仓)
git branch -D split-sdk split-plugins 2>/dev/null || true

# ============ 4. branch protection(要求 PR + 1 review + CI pass) ============
echo "=== 4/4 配置 main 分支保护 ==="
# 说明:3 仓 main 均要求 PR + 1 个 review + 状态检查通过。
# CI 在 todo 14 配置,届时把 required_status_checks.contexts 填上实际 job 名;
# 当前先启用 PR review 门,CI 就绪后补 contexts。
for repo in daedalus-core daedalus-sdk daedalus-plugins; do
    gh api -X PUT "repos/Daedalusys/${repo}/branches/main/protection" \
        -H "Accept: application/vnd.github+json" \
        -f "required_status_checks[strict]=true" \
        -f "required_status_checks[contexts][]=test" \
        -f "enforce_admins=true" \
        -f "required_pull_request_reviews[required_approving_review_count]=1" \
        -f "restrictions=null" \
        >/dev/null && echo "  ${repo}:main 分支保护已启用(PR + 1 review + CI)"
done

echo "=== 完成:3 仓已创建并推送 ==="
echo "  https://github.com/Daedalusys/daedalus-core"
echo "  https://github.com/Daedalusys/daedalus-sdk"
echo "  https://github.com/Daedalusys/daedalus-plugins"
echo "后续:todo 13 在 3 仓平级 clone 后配 go.work;todo 14 补 CI 后回填 required_status_checks.contexts。"