#!/bin/bash
# gssh.sh <port> <cmd...>  —— 经 Masterwet_Server 跳板连靶场 guest。
set -uo pipefail
port=$1; shift
LAB=${LAB:-$HOME/daedalus-lab}
exec ssh -T \
  -o ProxyCommand='ssh -W %h:%p Masterwet_Server' \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile="$LAB/known_hosts" \
  -o LogLevel=ERROR \
  -i "$LAB/keys/id_ed25519" \
  -p "$port" daedalus@127.0.0.1 "$@"
