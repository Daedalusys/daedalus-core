#!/bin/bash
# patch-guests.sh <port> —— 把新插件二进制与新策略装进 guest(就地替换,见 EVIDENCE.md)。
#
# 为什么是「就地替换」而不是重建镜像:本机 CPU 不支持 x86-64-v3,`just build` 跑不起来
# (`Fatal glibc error: CPU does not support x86-64-v3`)。镜像构建与 76 门禁由 CI 承担。
#
# 前置:在仓内先构建好二进制(全部带 -trimpath -buildvcs=false,与 release 腿同款):
#   daedalus-plugins: fs shell service dupe proc  -> /tmp/lab-bin/daedalus-<cap>
#   daedalus-core:    daedalus-audit daedalus-host daedalus-tx -> /tmp/lab-bin/
#   策略:daedalus-core/files/system/opt/daedalus/shared/policy.toml
set -euo pipefail
port=${1:?port}
LAB=${LAB:-$HOME/daedalus-lab}
BIN=${BIN:-/tmp/lab-bin}
POLICY=${POLICY:?指向 core 的生产 policy.toml}

SSH="ssh -T -o ProxyCommand=ssh -W %h:%p Masterwet_Server -o StrictHostKeyChecking=no \
 -o UserKnownHostsFile=$LAB/known_hosts -o LogLevel=ERROR -i $LAB/keys/id_ed25519 \
 -p $port daedalus@127.0.0.1"

# 1) /opt 在 bootc 的 composefs 上只读:开一个可写影子目录再 bind 回去
$SSH 'sudo mkdir -p /var/lib/daedalus-lab && sudo cp -a /opt/daedalus /var/lib/daedalus-lab/opt-daedalus && sudo mount --bind /var/lib/daedalus-lab/opt-daedalus /opt/daedalus'

# 2) 换二进制
for cap in fs shell service dupe proc; do
  $SSH "cat > /tmp/$cap.new" < "$BIN/daedalus-$cap"
  $SSH "sudo install -m 0755 -o root -g root /tmp/$cap.new /opt/daedalus/plugins/daedalus.$cap/bin/daedalus-$cap && rm -f /tmp/$cap.new"
done
for b in daedalus-audit daedalus-host daedalus-tx daedalus-shell daedalus-service; do
  $SSH "cat > /tmp/$b.new" < "$BIN/$b"
  $SSH "sudo install -m 0755 -o root -g root /tmp/$b.new /usr/local/bin/$b && rm -f /tmp/$b.new"
done

# 3) 换策略(所有读 policy 的二进制必须一起换,否则老二进制按 fail-closed 拒启动)
$SSH 'cat > /tmp/policy.toml.new' < "$POLICY"
$SSH 'sudo install -m 0644 -o root -g root /tmp/policy.toml.new /opt/daedalus/shared/policy.toml && rm -f /tmp/policy.toml.new'

# 4) 自检:策略能不能被新二进制读进去
$SSH 'sudo systemctl start daedalus-audit-rotate.service; systemctl is-failed daedalus-audit-rotate.service; sudo journalctl -u daedalus-audit-rotate --no-pager -n 2'
