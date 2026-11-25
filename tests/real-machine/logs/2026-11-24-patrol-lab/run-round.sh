#!/bin/bash
# run-round.sh <tag> [binary]  —— 跑一轮巡检并把退出码/邮件数打出来（靶场用）。
set -uo pipefail
L=/mnt/cache/tmp/opencode/patrol-lab
tag=${1:?tag}
bin=${2:-$L/bin-t9/daedalus-patrol}
export DAEDALUS_FLEET_DIR=$L/fleet
export DAEDALUS_FLEET_CLIENT_CERT=$L/pki/client.pem
export DAEDALUS_FLEET_CLIENT_KEY=$L/pki/client.key
export DAEDALUS_FLEET_CA=$L/pki/ca.pem
export PATH=$HOME/.bun/bin:$PATH
before=$(ls -1 $L/mails/*.eml 2>/dev/null | wc -l)
cd $L/run || exit 9
start=$(date +%s)
$bin run --node lab-a-1e5d63a9 --config $L/run/patrol.toml --state-dir $L/run/state --round-root $L/run/round > $L/run/$tag.out 2> $L/run/$tag.err
rc=$?
end=$(date +%s)
after=$(ls -1 $L/mails/*.eml 2>/dev/null | wc -l)
echo "tag=$tag rc=$rc secs=$((end-start)) mails_before=$before mails_after=$after"
echo "--- stdout ---"; tail -6 $L/run/$tag.out
echo "--- stderr ---"; tail -6 $L/run/$tag.err
