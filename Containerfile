# Allow build scripts to be referenced without being copied into the final image
FROM scratch AS ctx

COPY base_image/files/system /system_files/
COPY base_image/files/scripts /build_files/
COPY base_image/*.pub /keys/

# Base Image
FROM quay.io/almalinuxorg/almalinux-bootc:10@sha256:d8679e022ff2b9f9873becf262e4447beb5b0551a8dc83c146e2e6f27bd5183f

ARG IMAGE_NAME=daedalus-os
ARG IMAGE_REGISTRY=localhost
ARG VARIANT=kde
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
# 改这里极罕见(基本是同步上游 base_image),命中 cache 概率高
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,target=/var/cache/dnf,sharing=locked \
    /ctx/build_files/build.sh 10 20 30 40 50

# === Stage 2:Daedalus 目录结构(60-ai-middleware + 63-object-model-state) ===
# 纯 mkdir + chown,几乎不变
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh 60 63

# === Stage 3:AI 安全基础(65-ai-safety) — dnf install curl/unzip/deno ===
# 改这里:通常不会变(deno 版本固定);变了就整个 stage 重跑
# cache mount:dnf 包跨 build 缓存(避重复下载)。依赖 Stage 0.5 已切阿里云。
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,target=/var/cache/dnf,sharing=locked \
    /ctx/build_files/build.sh 65

# === Stage 4:Daedalus 服务(70 + 70a + 75) ===
# 改这里:极罕见(只有 plugin manifest / systemd unit 改动才触发)
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh 70 70a 75

# === Stage 5:Daedalus 插件生成(76-daedalus-plugin-gen) ===
# 改这里:中等频率(改 systemd ExecStart / 插件接线)
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh 76

# === Stage 6:xrdp 装包(77-xrdp) — dnf install epel-release + xrdp ===
# 改这里:经常调(xrdp 配置 / firewall / 用户组)
# cache mount:dnf 包跨 build 缓存
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,target=/var/cache/dnf,sharing=locked \
    /ctx/build_files/build.sh 77

# === Stage 6.5:注入开发默认用户(78-dev-user)—— xrdp(77) 之后、签名(90) 之前 ===
# 建 daedalus(wheel + 可选 xrdp 组)+ 免密 sudo + root 密码 + 开 sshd。bootc 基础
# 镜像无账号 → 之前 SSH 连得上 sshd 却 Permission denied 就是缺这个 stage。排在 77
# 后是因为要加进 xrdp 组(由 77 的 xrdp 包创建)。凭据见 README.md。
# 79=sddm 自启
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh 78 79

# === Stage 7:签名 + image info(90 + 91) ===
# 改这里:罕见(签名 key 改动 / image-info 格式改动)
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
