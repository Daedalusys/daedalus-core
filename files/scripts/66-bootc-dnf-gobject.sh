#!/usr/bin/env bash

set -xeuo pipefail

# python3-gobject-base 是 dnf 在 bootc/ostree 节点上的运行时后端依赖。
# dnf 用 /run/ostree-booted 判定自己跑在只读部署里, 该判定经 dnf.util._BootcSystem
# 调 GObject 内省; 缺这个包时 `import gi` 直接抛 Python traceback, 用户看到的不是
# 「不可变节点不能装包」, 而是一屏栈 —— package.set 的 fail-closed 结论因此失真。
#
# 为什么放在独立的 Daedalus stage(66), 而不是 vendor 的 10-50:
#   - 10-50 是上游 atomic-desktop 的同步脚本, 改动会在下次 sync 被覆盖;
#   - 也不塞进 65-ai-safety: 那里是 AI 工具链, 这条是纯运行时依赖;
#   - 独立成层后, 改它只失效这一层, 不连带重跑 AI 工具链的下载。
dnf install -y python3-gobject-base

# 构建期自证: 包被解析安装 != import 真的可用(依赖可能装成半套)。
# 这里失败即构建失败, 问题不留给用户第一次 dnf。
python3 -c 'import gi'

echo "66-bootc-dnf-gobject.sh: python3-gobject-base 已安装且 import gi 可用"
