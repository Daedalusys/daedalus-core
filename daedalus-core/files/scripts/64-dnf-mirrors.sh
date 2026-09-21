#!/usr/bin/env bash

set -xeuo pipefail

# Daedalus 扩展:把 dnf 仓库切到国内镜像(Aliyun),build 在国内机器上跑速度
# 从 ~MB/s 级提到 ~百 MB/s。
#
# 踩坑史(按时间顺序,每次都修不全):
#  1. 最初只 sed mirrorlist= → bootc 镜像用 metalink= 不是 mirrorlist=,漏了
#  2. sed 也处理 metalink= → 但 bootc 镜像的 .repo 文件常常**只有 metalink= 没
#     baseurl= 行**,只 sed 替换在没 baseurl 时不生效
#  3. 加 grep -q baseurl + echo 追加 → 但 bootc 镜像的 appstream 仓库定义
#     **不在 /etc/yum.repos.d/ 里**,而是在 /etc/dnf/dnf.conf 内嵌 section
#  4. 加 dnf.conf 扫描 + cat 验证 → dnf 居然**还**有第 4 处来源(可能是
#     /usr/share/dnf/repos/、modular repo、/etc/dnf/repos.d/...?)
#
# 核弹方案(本版):**不再修任何旧文件**。直接:
#  a) 禁用所有现有的 .repo 文件(enabled=0)
#  b) 禁用 /etc/dnf/dnf.conf 里所有非 [main] section
#  c) 创建全新的 /etc/yum.repos.d/daedalus-aliyun.repo,所有需要的 repo 都走阿里云
#  d) dnf proxy 兜底
#  e) 收尾跑 dnf repolist 打印启用的 repo,build log 一眼能看出对错

ALIYUN_BASE="https://mirrors.aliyun.com"

# === 步骤 1:禁用所有现有的 .repo 文件 ===
# 保留文件名,只改 enabled=1 → enabled=0(其他字段不动,排查时还能看原配置)
for repo_file in /etc/yum.repos.d/*.repo; do
    if [ -f "${repo_file}" ]; then
        sed -i 's|^enabled=1|enabled=0|' "${repo_file}"
        echo "64-dnf-mirrors: disabled ${repo_file}"
    fi
done

# === 步骤 2:禁用 /etc/dnf/dnf.conf 里所有非 [main] section ===
# bootc 镜像常把 repo 定义内嵌在 dnf.conf(像 [appstream]、[extras] 等等)。
# 用 awk 重写:遇到 [main] 保留原样;遇到其他 [xxx] 整段 enabled=1 → enabled=0;
# 非 section 内行(应该没有)原样。
DNF_CONF="/etc/dnf/dnf.conf"
if [ -f "${DNF_CONF}" ]; then
    # 备份方便回滚/debug
    cp -p "${DNF_CONF}" "${DNF_CONF}.bak.64-dnf-mirrors"
    awk '
        BEGIN { in_non_main = 0 }
        /^\[main\]/ { in_non_main = 0; print; next }
        /^\[/        { in_non_main = 1; print; next }
        in_non_main && /^enabled=1/ { print "enabled=0"; next }
        { print }
    ' "${DNF_CONF}" > "${DNF_CONF}.new" && mv "${DNF_CONF}.new" "${DNF_CONF}"
    echo "64-dnf-mirrors: disabled all non-[main] sections in ${DNF_CONF} (备份 .bak.64-dnf-mirrors)"
fi

# === 步骤 3:创建全新的 daedalus-aliyun.repo(所有需要的 repo 都走阿里云) ===
# dnf 找 repo 名时优先用启用的 .repo,新文件 enabled=1,旧的全 disabled=0,
# 名字即使重复 dnf 也用启用的(daedalus- 前缀避免与原名冲突)。
cat > /etc/yum.repos.d/daedalus-aliyun.repo <<'REPO_EOF'
[daedalus-aliyun-baseos]
name=Daedalus Aliyun BaseOS (AlmaLinux 10)
baseurl=https://mirrors.aliyun.com/almalinux/10/BaseOS/x86_64/os/
enabled=1
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-AlmaLinux-10

[daedalus-aliyun-appstream]
name=Daedalus Aliyun AppStream (AlmaLinux 10)
baseurl=https://mirrors.aliyun.com/almalinux/10/AppStream/x86_64/os/
enabled=1
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-AlmaLinux-10

[daedalus-aliyun-extras]
name=Daedalus Aliyun Extras (AlmaLinux 10)
baseurl=https://mirrors.aliyun.com/almalinux/10/extras/x86_64/os/
enabled=1
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-AlmaLinux-10

[daedalus-aliyun-highavailability]
name=Daedalus Aliyun HighAvailability (AlmaLinux 10)
baseurl=https://mirrors.aliyun.com/almalinux/10/HighAvailability/x86_64/os/
enabled=1
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-AlmaLinux-10

[daedalus-aliyun-crb]
name=Daedalus Aliyun CRB (AlmaLinux 10)
baseurl=https://mirrors.aliyun.com/almalinux/10/CRB/x86_64/os/
enabled=1
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-AlmaLinux-10

[daedalus-aliyun-epel]
name=Daedalus Aliyun EPEL 10
baseurl=https://mirrors.aliyun.com/epel/10/Everything/x86_64/
enabled=1
gpgcheck=1
# 注意:不能用 file:///etc/pki/rpm-gpg/RPM-GPG-KEY-EPEL-10 —— 那个 key 文件由
# epel-release 包安装,而本脚本(64)跑在 stage 0.5(装 epel-release 之前),
# 文件尚不存在,dnf 刷 EPEL 元数据时会 Curl error 37 直接 exit 1。
# 改用阿里云镜像上的 HTTPS key URL(dnf 会自动下载导入)。
gpgkey=https://mirrors.aliyun.com/epel/RPM-GPG-KEY-EPEL-10
REPO_EOF
echo "64-dnf-mirrors: created /etc/yum.repos.d/daedalus-aliyun.repo (6 repos 全部阿里云)"

# === 步骤 4:dnf proxy 兜底(任何漏网的 repo 走 host proxy) ===
if [ -n "${HTTP_PROXY:-}" ]; then
    echo "proxy=${HTTP_PROXY}" >> /etc/dnf/dnf.conf
    echo "64-dnf-mirrors: dnf proxy=${HTTP_PROXY} (兜底)"
fi

# === 步骤 5:验证 — dnf repolist 一眼能看出启用了哪些 repo ===
echo "===== 64-dnf-mirrors 验证 dnf repolist ====="
dnf --disablerepo="*" --enablerepo="daedalus-aliyun-*" repolist 2>&1 || true
echo "===== 验证完毕 ====="

echo "64-dnf-mirrors: 完成(nuclear: disable all + create 1 fresh .repo + dnf.conf inline disable)"
