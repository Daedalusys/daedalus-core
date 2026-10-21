# Allow build scripts to be referenced without being copied into the final image
# Base Image — headless fork of almalinux-bootc (kitten variant + sshd/NetworkManager).
# See https://github.com/Daedalusys/almalinux-bootc (plan 2026-10-20).
# Fork ships ONLY the 10-kitten variant; the upstream full :10 base is no longer used.
# Override via --build-arg HEADLESS_BASE_IMAGE=... for testing local builds.
# Default pins the digest from the first successful fork CI build (2026-10-20):
#   ghcr.io/daedalusys/almalinux-bootc:10-kitten
#   digest sha256:a4bff88738b674c666277de8ffbc64c8e59deedfcc9a054b55d785428f877040
# Pin digest for reproducibility; bump digest + retest when upstream ships new fork release.
ARG HEADLESS_BASE_IMAGE=ghcr.io/daedalusys/almalinux-bootc@sha256:a4bff88738b674c666277de8ffbc64c8e59deedfcc9a054b55d785428f877040

FROM scratch AS ctx

COPY base_image/files/system /system_files/
COPY base_image/files/scripts /build_files/
COPY base_image/*.pub /keys/

FROM ${HEADLESS_BASE_IMAGE}

ARG IMAGE_NAME=daedalus-os
ARG IMAGE_REGISTRY=localhost
# VARIANT=headless 唯一支持;kde 已废弃(2026-10-17 fleet 改造)。
# 镜像默认无图形登录链(sddm/plasma/kwin 不装),daedalus-host rescue 经 RDP/VNC
# 现场拉起(headless 默认 xrdp masked,enable-vnc 显式开)。要本地 KDE 开发请另起
# 分支并恢复 ARG VARIANT=kde。
ARG VARIANT=headless
ARG TARGETARCH

# ARG 只做 Dockerfile 指令里的 ${VAR} 文本替换,不会进 RUN 子进程环境。
# 20-desktop.sh / 其它 build 脚本用 shell 读 $VARIANT / $TARGETARCH,必须是真正的
# 环境变量,否则 VARIANT 为空 → 走 else 分支 → KDE/sddm 从没装上(镜像里 rpm -q
# sddm 不存在的根因)。这里显式 ENV 一遍,贯穿所有 stage 的 RUN。
ENV VARIANT=${VARIANT}
ENV TARGETARCH=${TARGETARCH}

# Daedalus 扩展:把单个 RUN 拆成多 stage,每个 stage 只跑对应脚本。
# 调试效率:build 出错时 build log 明确显示哪个 stage 挂了(不再是"build.sh 整个
# 跑完才报错,不知道哪一步");改单个脚本(如 77-xrdp.sh)只重跑对应 stage,
# 不重跑 65-ai-safety / 70-mcp-servers / 76-plugin-gen 等无关阶段。
#
# dnf cache mount 只在真正装包的 stage(65、77)挂,其他 stage 不挂,减少
# 不必要的 cache 锁竞争。
#
# 共享约束:
# - 每个 stage 的 system_files 拷贝用 /var/lib/daedalus/.build_init_done
#   sentinel 守住,只在第一个 stage 真正执行;
# - cleanup 只在最后一个 stage 跑(--finalize 显式触发),中间 stage 跳过
#   防止误删后续 stage 依赖的文件;
# - /tmp tmpfs 每个 stage 重建,不要放 stage 间共享数据;
# - 阶段间状态通过 rootfs 共享(每个 RUN 的输出是后续 RUN 的输入)。
#
# 注意:/opt 未挂载为 tmpfs,因此 /opt/daedalus(插件安装态 plugins/ 等)持久化于不可变 rootfs

# === Stage 0:Init — 拷贝 system files + /keys(只需一次) ===
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh --init

# === Stage 0.5:dnf 国内镜像(64-dnf-mirrors)—— 必须在任何 dnf install 之前 ===
# 拆 stage 后 build.sh 按编号白名单执行,漏列的脚本【不会跑】。64 切阿里云 + 写
# dnf proxy,须在 10-50/65/77 的 dnf install 之前,否则 stage 1 的
# dnf-command(config-manager)/epel-release 就走国际源卡到 kB/s 级。
# 64 本身零依赖(纯 sed 改 repo + cat 写新 repo 文件),只要求 stage 0 文件就位。
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,target=/var/cache/dnf,sharing=locked \
    /ctx/build_files/build.sh 64

# === Stage 1:Base OS(vendor 上游脚本 10-50) ===
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,target=/var/cache/dnf,sharing=locked \
    /ctx/build_files/build.sh 10 20 30 40 50

# === Stage 2:Daedalus 目录结构(60-ai-middleware + 63-object-model-state) ===
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh 60 63

# === Stage 3:AI 安全基础(65-ai-safety) — dnf install curl/unzip/deno ===
# 依赖 Stage 0.5 已切国内源。
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,target=/var/cache/dnf,sharing=locked \
    /ctx/build_files/build.sh 65

# === Stage 4:Daedalus 服务(70 + 70a + 75) ===
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh 70 70a 75

# === Stage 5:Daedalus 插件生成(76-daedalus-plugin-gen) ===
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh 76

# === Stage 6:xrdp 装包(77-xrdp) — dnf install epel-release + xrdp ===
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,target=/var/cache/dnf,sharing=locked \
    /ctx/build_files/build.sh 77

# === Stage 6.5:注入开发默认用户(78-dev-user)—— xrdp(77) 之后、签名(90) 之前 ===
# 建 daedalus(wheel + 可选 xrdp 组)+ 免密 sudo + root 密码 + 开 sshd。bootc 基础
# 镜像无账号 → 之前 SSH 连得上 sshd 却 Permission denied 就是缺这个 stage。排在 77
# 后是因为要加进 xrdp 组(由 77 的 xrdp 包创建)。凭据见 README.md。
# 79=sddm 自启 已删,headless 默认无图形登录链(见 79-sddm-autostart.sh 移除记录)。
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh 78

# T2 配合: headless 变体打标, daedalus-host rescue 可识别。
# /etc/os-release 由 almalinux-bootc 基础镜像生成,默认无 VARIANT 字段;此处追加
# 一行供下游 T2 (cmd/daedalus-host rescue) 识别 "headless 镜像",T3 recipe 也
# 校验该标记(见 tests/build/headless-variant.sh)。
RUN printf '\nVARIANT=headless\n' >> /etc/os-release

# === Stage 6.6:启用 P4 controller systemd unit(80-daedalus-controller) ===
# controller unit 是 daedalus-host-controller.service,镜像构建期 systemctl
# enable 写入 preset,bootc 启动后自动拉起。在 host unit 之后(78-79)、
# 签名(90)之前,不在 finalize 阶段(stage 8)。
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh 80

# === Stage 7:签名 + image info(90 + 91) ===
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh 90 91

# === Stage 8:最终 cleanup(只在这一个 stage 跑) ===
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh --finalize

### LINTING
## Verify final image and contents are correct.
RUN bootc container lint
