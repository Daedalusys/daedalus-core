#!/usr/bin/env bash
set -xeuo pipefail

# Support DRY_RUN environment variable: when "${DRY_RUN:-0}" = "1", pass --dry-run to rsync
DRY_RUN_FLAG=""
if [ "${DRY_RUN:-0}" = "1" ]; then
    DRY_RUN_FLAG="--dry-run"
fi

# 纵深防御排除(todo 15,计划验收 #3 镜像零残留):测试/缓存类文件绝不进 vendor 树,
# 即便未来某任务误把 *.test.ts / test_*.py / __pycache__ / *.pyc 落回 daedalus/files/
# 或 daedalus/plugin/ 源树,也不会被同步进 base_image/(进而是构建上下文)。
# 注:todo 5/11/14 已把 py 服务器与 deno 测试迁出,当前四条规则均为纯防御性空转。
EXCLUDES=(
    --exclude='__pycache__'
    --exclude='*.pyc'
    --exclude='*.test.ts'
    --exclude='test_*.py'
)

echo "=== Syncing Daedalusfiles into base_image ==="

# 0. Bootstrap base_image/(CI runner 必需; 本地存在时跳过):
#    上游 vendor 树不追踪进 git(repo .gitignore 第 4 行 base_image/), 但 sync 的目标必须是
#    真实存在的目录, 否则 rsync rc=11 失败。CI 上从 AlmaLinux/atomic-desktop@main
#    浅克隆补充, 本地仓库已有 base_image/(同步自有)时跳过以免重写开发环境工作副本。
DAEDALUS_BASE_IMAGE_UPSTREAM="${DAEDALUS_BASE_IMAGE_UPSTREAM:-https://github.com/AlmaLinux/atomic-desktop.git}"
DAEDALUS_BASE_IMAGE_BRANCH="${DAEDALUS_BASE_IMAGE_BRANCH:-main}"
if [ ! -d base_image ] && [ "${DAEDALUS_SKIP_BASE_IMAGE_BOOTSTRAP:-0}" != "1" ]; then
    if [ -n "${DRY_RUN_FLAG}" ]; then
        echo "=== [DRY-RUN] Would bootstrap base_image from ${DAEDALUS_BASE_IMAGE_UPSTREAM}@${DAEDALUS_BASE_IMAGE_BRANCH} (shallow) ==="
    else
        echo "=== Bootstrapping base_image from ${DAEDALUS_BASE_IMAGE_UPSTREAM}@${DAEDALUS_BASE_IMAGE_BRANCH} (shallow) ==="
        git clone --depth 1 --branch "${DAEDALUS_BASE_IMAGE_BRANCH}" "${DAEDALUS_BASE_IMAGE_UPSTREAM}" base_image
    fi
fi

# 1. General copy
rsync -a ${DRY_RUN_FLAG} "${EXCLUDES[@]}" daedalus/files/system/ base_image/files/system/
rsync -a ${DRY_RUN_FLAG} "${EXCLUDES[@]}" daedalus/files/scripts/ base_image/files/scripts/

# 2. opt/daedalus exclusive sync with --delete
rsync -a --delete ${DRY_RUN_FLAG} "${EXCLUDES[@]}" daedalus/files/system/opt/daedalus/ base_image/files/system/opt/daedalus/

# 3. Targeted sync for systemd daedalus-* units with PREFIX-SCOPED stale-delete (todo 5 遗留收口):
#    源目录整体同步(不再是 shell glob),--delete 的杀伤面被 include/exclude 规则钳制在
#    daedalus-* 前缀内:接收端不匹配任何 include 的上游单元(如 system-flatpak-setup.service)
#    落入 --exclude='*' → rsync 默认不删除被排除文件 → 上游资产零风险;
#    而接收端残留的失效 daedalus-* 单元(daedalus-{fs,shell}-deno.service 等)在源端已不存在,
#    命中 daedalus-* include → 被 stale-delete 清除。daedalus-*.service.d/ 目录内容
#    由 'daedalus-*/**' 规则随行同步/清除。
rsync -a --delete ${DRY_RUN_FLAG} "${EXCLUDES[@]}" \
    --include='daedalus-*' --include='daedalus-*/**' --exclude='*' \
    daedalus/files/system/usr/lib/systemd/system/ base_image/files/system/usr/lib/systemd/system/

# 4. 插件源码态目录同步(todo 11 三层迁移, 决策 23/24):
#    daedalus/plugin/ → base_image/plugin/ 仅作为构建上下文存档;
#    Containerfile 只 COPY base_image/files/{system,scripts},因此 base_image/plugin/
#    绝不进镜像 rootfs /opt——镜像内插件安装态由 plugin-pack / pack-copilot-plugin.sh
#    经 Pack→Verify 生成到 daedalus/files/system/opt/daedalus/plugins/。
#    todo 15 起追加 --delete:base_image/plugin/ 为 Daedalus 独占目录(todo 11 新建,
#    无上游资产混居),源端删除的插件目录可安全自洁,不存在 systemd 段的误删半径问题。
#    --delete-excluded 同样只作用于本段:被 EXCLUDES 排除的接收端残留(如 todo 14
#    迁移前滞留的 copilot/*.test.ts)rsync 默认不删(排除即豁免),此处显式撤销豁免,
#    让"排除进镜像"与"清除 vendor 残留"共用同一套模式,不留自洁死角。
rsync -a --delete --delete-excluded ${DRY_RUN_FLAG} "${EXCLUDES[@]}" daedalus/plugin/ base_image/plugin/

# 5. Validate after sync: ensure base_image/files/system/usr/lib/systemd/system/system-flatpak-setup.service exists
if [ ! -f base_image/files/system/usr/lib/systemd/system/system-flatpak-setup.service ]; then
    echo "ERROR: base_image upstream file system-flatpak-setup.service was unexpectedly removed or missing!" >&2
    exit 1
fi

echo "=== Daedalusfiles sync completed successfully ==="
