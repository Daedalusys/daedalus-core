#!/bin/bash

# Daedalus 扩展:在原 vendor build.sh 基础上,支持**只跑部分脚本** + 第一次拷贝 +
# 最后 cleanup。Containerfile 把 build 拆成多个 RUN stage,每个 stage 调本脚本
# 时只传对应脚本前缀(65/77/...),用 find 匹配文件名,只跑那些。
#
# 用法:
#   build.sh                    # 跑所有(向后兼容,等价于旧行为)
#   build.sh 65                 # 只跑 65-ai-safety.sh
#   build.sh 70 70a 75          # 跑这三个
#   build.sh --init             # 只做 system_files 拷贝(第一个 stage 调用)
#   build.sh --finalize         # 只跑 cleanup(最后一个 stage 调用)
#
# 行为细节:
# - system_files 拷贝用 sentinel /var/lib/daedalus/.build_init_done 防重,
#   只在第一个 stage 真正执行(后续 stage 看到这个文件就跳过);
# - cleanup 只在 $# -eq 0 时跑(即"全跑"模式,或 --finalize 显式触发),
#   防止中间 stage 也跑 cleanup 删了后续 stage 需要的东西;
# - set -ouex pipefail 保持 vendor 行为(管道错误立即失败)。

set -euo pipefail
set -x

CONTEXT_PATH="$(realpath "$(dirname "$0")/..")"   # /ctx
BUILD_SCRIPTS_PATH="$(realpath "$(dirname "$0")")" # /ctx/build_files

# === 第一次 stage:拷贝 system files(sentinel 防重) ===
if [ ! -f /var/lib/daedalus/.build_init_done ]; then
    printf "::group:: === Copying files ===\n"
    cp -avf "${CONTEXT_PATH}/system_files/." /
    mkdir -p /var/lib/daedalus
    touch /var/lib/daedalus/.build_init_done
    printf "::endgroup::\n"
fi

# === 决定跑哪些脚本 ===
# --init:只做拷贝(已 sentinel 守住,这里也跳)
# --finalize:只跑 cleanup
# 否则:按 pattern 找匹配脚本(prefix 匹配,如 "65" → 65-ai-safety.sh)
if [ "${1:-}" = "--init" ]; then
    exit 0
fi

if [ "${1:-}" = "--finalize" ]; then
    printf "::group:: === Image Cleanup ===\n"
    "${BUILD_SCRIPTS_PATH}/cleanup.sh"
    printf "::endgroup::\n"
    exit 0
fi

if [ $# -eq 0 ]; then
    # 向后兼容:跑所有
    mapfile -t scripts < <(find "${BUILD_SCRIPTS_PATH}" -maxdepth 1 -iname "*-*.sh" -type f | sort --sort=human-numeric)
else
    # 按 prefix 匹配(支持多个)
    scripts=()
    for pattern in "$@"; do
        while IFS= read -r f; do
            scripts+=("$f")
        done < <(find "${BUILD_SCRIPTS_PATH}" -maxdepth 1 -iname "${pattern}*.sh" -type f 2>/dev/null | sort --sort=human-numeric)
    done
    # 去重并保持人序:先按整行 sort -u 去重,再 --sort=human-numeric 排执行顺序。
    # 切勿写成 `sort --sort=human-numeric -u`:human-numeric 对以 "base_image/…" 这类
    # 非数字开头的全路径排序键全算相同 → -u 把多个脚本压成 1 个,导致 20-desktop /
    # 70a / 75 等被静默跳过(镜像缺 KDE 的元凶)。
    mapfile -t scripts < <(printf '%s\n' "${scripts[@]}" | sort -u | sort --sort=human-numeric)
fi

# === 跑选中的脚本 ===
for script in "${scripts[@]}"; do
    printf "::group:: === $(basename "$script") ===\n"
    "$(realpath "$script")"
    printf "::endgroup::\n"
done

# === 全跑模式(无参)才执行 cleanup ===
# 中间 stage 跑单个或几个脚本时,cleanup 不能跑(会误删后续 stage 依赖)。
if [ $# -eq 0 ]; then
    printf "::group:: === Image Cleanup ===\n"
    "${BUILD_SCRIPTS_PATH}/cleanup.sh"
    printf "::endgroup::\n"
fi
