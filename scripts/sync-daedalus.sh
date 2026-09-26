#!/usr/bin/env bash
set -xeuo pipefail

# Support DRY_RUN environment variable: when "${DRY_RUN:-0}" = "1", pass --dry-run to rsync
DRY_RUN_FLAG=""
if [ "${DRY_RUN:-0}" = "1" ]; then
    DRY_RUN_FLAG="--dry-run"
fi

# 镜像零残留纵深防御:测试/缓存类文件绝不进 vendor 树(即构建上下文),即便源树里
# 又落回 *.test.ts / test_*.py 也不会被同步。当前四条规则均为纯防御性空转。
EXCLUDES=(
    --exclude='__pycache__'
    --exclude='*.pyc'
    --exclude='*.test.ts'
    --exclude='test_*.py'
)

echo "=== Syncing Daedalusfiles into base_image ==="

# Bootstrap base_image/:sync 目标目录必须真实存在,否则 rsync rc=11 失败;本地已有
# 时不重复克隆,以免重写开发环境工作副本。
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

# EL10 上上游 20-desktop.sh 的 kde 分支无法 enable sddm:KDE 组未随组安装 sddm,
# 且 sddm-wayland-plasma 可能已把 display-manager.service 软链占为己用(systemd
# 拒绝覆盖软链)。用精确锚点 sed 替换 + 自校验:锚点漂移(上游已修好)时不盲改、直接失败。
KDE_PATCH_TARGET="base_image/files/scripts/20-desktop.sh"
if [ -f "${KDE_PATCH_TARGET}" ] && grep -q 'systemctl enable sddm' "${KDE_PATCH_TARGET}"; then
    if ! grep -q 'dnf install -y sddm' "${KDE_PATCH_TARGET}"; then
        echo "=== Patching upstream 20-desktop.sh: kde 分支显式安装 sddm ==="
        sed -i 's|^    systemctl enable sddm$|    # Daedalus 补丁: EL10 的 KDE 组未随组装出 sddm, 显式安装\n    dnf install -y sddm\n    # Daedalus 补丁 2: sddm-wayland-plasma 已配好 display-manager.service\n    # (软链 plasmalogin.service); 已存在则跳过 enable, 避免 systemd 拒绝覆盖软链\n    if [ ! -e /etc/systemd/system/display-manager.service ]; then\n        systemctl enable sddm\n    fi|' "${KDE_PATCH_TARGET}"
        grep -q 'dnf install -y sddm' "${KDE_PATCH_TARGET}" || { echo "ERROR: kde sddm 补丁替换失败(锚点漂移?)" >&2; exit 1; }
    fi
fi

# rsync 源一律用仓根相对路径:调用契约是 cwd = daedalus-core 仓根(justfile `sync:`)。
rsync -a ${DRY_RUN_FLAG} "${EXCLUDES[@]}" files/system/ base_image/files/system/
rsync -a ${DRY_RUN_FLAG} "${EXCLUDES[@]}" files/scripts/ base_image/files/scripts/

rsync -a --delete ${DRY_RUN_FLAG} "${EXCLUDES[@]}" files/system/opt/daedalus/ base_image/files/system/opt/daedalus/

# systemd 段用 PREFIX-SCOPED stale-delete:--delete 的杀伤面被 include/exclude 钳制在
# daedalus-* 前缀内,不匹配 include 的上游单元落入 --exclude='*' → rsync 默认不删除被
# 排除文件 → 上游资产零风险;源端已删除的失效 daedalus-* 单元命中 include,被清除。
rsync -a --delete ${DRY_RUN_FLAG} "${EXCLUDES[@]}" \
    --include='daedalus-*' --include='daedalus-*/**' --exclude='*' \
    files/system/usr/lib/systemd/system/ base_image/files/system/usr/lib/systemd/system/

# 插件源码态同步到 base_image/plugin/,仅作为构建上下文存档:Containerfile 只 COPY
# base_image/files/{system,scripts},故它绝不进镜像;镜像内安装态由 Pack→Verify 生成。
# 源仓缺失时跳过本腿(CI 镜像 job 不检出插件仓),仅影响 archive 存档,零镜像语义。
# --delete 安全:base_image/plugin/ 是 Daedalus 独占目录,无上游资产混居。
# --delete-excluded 撤销 EXCLUDES 的删除豁免,让"排除进镜像"与"清除 vendor 残留"共用一套模式。
PLUGIN_SRC="../daedalus-plugins"
if [ -d "${PLUGIN_SRC}" ]; then
    rsync -a --delete --delete-excluded ${DRY_RUN_FLAG} "${EXCLUDES[@]}" "${PLUGIN_SRC}/" base_image/plugin/
else
    echo "note: ${PLUGIN_SRC} 源仓不存在(CI 镜像 job 不检出插件仓 / 非三仓平级布局),跳过 plugin 源存档同步腿"
fi

# 校验 sync 未误伤上游资产。
if [ ! -f base_image/files/system/usr/lib/systemd/system/system-flatpak-setup.service ]; then
    echo "ERROR: base_image upstream file system-flatpak-setup.service was unexpectedly removed or missing!" >&2
    exit 1
fi

echo "=== Daedalusfiles sync completed successfully ==="
