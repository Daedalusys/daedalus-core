#!/usr/bin/env bash
#
# 目标机侧审计取证的唯一实现:数 agent 本地哈希链(/var/lib/daedalus-agent/
# audit/chain.jsonl)的行数。
#
# 为什么只信这条链:「这条 intent 到没到过这台机」在 agent 侧唯一可信的凭据就是
# 它 —— 链上没有新行 = RPC 没到(fail-closed 的拒绝也会留一行,故"没有记录"本身
# 就是证据)。控制面的 status / tx_id 在失败时同样给不出凭据,不能用来代替。
#
# 本文件被 tests/fleet/plan-c-e2e.sh 混合腿与 tests/fleet/agent-e2e.sh 高危腿
# 共用:两边口径必须同一份实现,不许各写一套。

# agent_audit_count <ssh 别名> [ssh 调用及其选项...]:agent 本地哈希链的行数。
#
# 第二段起是完整的 ssh 调用(如 ssh -n / ssh -o BatchMode=yes),缺省只调 ssh。
# 各驱动按自己的连接口径传,不在这里猜;用 "$@" 传递,不做字符串再切分(带空格的
# 命令串在 zsh 下不切词,会变成"command not found")。
#
# 输出:纯数字行数。ssh 不通时输出为空(调用方必须据"空"记 FAIL,不许当 0)。
agent_audit_count() {
    local alias="$1"; shift
    local -a sshcmd=("$@")
    if [ "${#sshcmd[@]}" -eq 0 ]; then
        sshcmd=(ssh)
    fi
    # 重定向必须由 root 那侧的 shell 做(`sudo wc -l < file` 会被登录用户的
    # shell 抢先重定向 → Permission denied),故改用 cat | wc -l。
    "${sshcmd[@]}" "$alias" \
        'sudo cat /var/lib/daedalus-agent/audit/chain.jsonl 2>/dev/null | wc -l' |
        tr -dc '0-9'
}
