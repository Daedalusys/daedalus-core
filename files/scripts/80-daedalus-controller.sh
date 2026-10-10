#!/bin/bash
# 80-daedalus-controller.sh:
#   启用 P4 controller runtime 的 systemd unit(daedalus-host-controller.service)。
#   与 host unit 解耦(Wants= 而非 Requires=):controller 失败不拖死 host。
#   本脚本幂等:systemctl enable 在镜像构建期把符号链写入 preset,后续 image
#   启动时由 systemd 自动启动。
set -euo pipefail

UNIT="daedalus-host-controller.service"

# unit 文件本身由 files/system/usr/lib/systemd/system/ 提供,build stage 0
# 已 cp -avf 到 /;这里只确保 enable。
if [ ! -f "/usr/lib/systemd/system/$UNIT" ]; then
    echo "80-daedalus-controller.sh: 缺少 $UNIT unit 文件(应由 Stage 0 引入)" >&2
    exit 1
fi

systemctl enable "$UNIT"

echo "80-daedalus-controller.sh: $UNIT 已 enable"