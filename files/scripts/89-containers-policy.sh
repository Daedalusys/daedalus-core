#!/usr/bin/env bash
# 补出 containers-common 的默认镜像签名策略,供上游 90-signing.sh 改写。
set -euo pipefail

POLICY_FILE="/etc/containers/policy.json"

# headless fork base(almalinux-bootc 10-kitten)不含 containers-common,该文件不存在;
# 上游 90-signing.sh 用 jq 以**现有文件为输入**合并签名规则,缺文件即 exit 2。装
# containers-common 会把 crun/runc/container-selinux(~4MiB)拉进一个不跑容器的主机镜像,
# 所以这里只落一份与 EL10 打包内容等价的默认策略(去掉指向不存在 key 的 redhat 规则)。
if [ -f "${POLICY_FILE}" ]; then
    echo "policy.json 已存在(containers-common 在 base 里),89 跳过"
    exit 0
fi

mkdir -p /etc/containers
cat > "${POLICY_FILE}" <<'EOF'
{
    "default": [
        {
            "type": "insecureAcceptAnything"
        }
    ],
    "transports": {
        "docker-daemon": {
            "": [
                {
                    "type": "insecureAcceptAnything"
                }
            ]
        }
    }
}
EOF
