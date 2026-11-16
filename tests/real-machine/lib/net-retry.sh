#!/usr/bin/env bash
# 网络调用的「有界 + 重试」薄封装:唯一对外接口是 net_retry。
#
# 为什么需要:实机腿的镜像拉取/摘要查询原先没有预算,一次挂住的请求会一路吃到
# 整条腿的 job 时限;反过来把「慢但会成功」的拉取一刀切成短超时又永远拉不完
# (实测一次 2.25 GB 拉取在宿主被 6 台 KVM 抢资源时跑了 42 分钟才成功)。
# 所以既要有界,也要留重试余地 —— 单次预算宽、次数少,不走「短超时 + 无限重试」。
#
# 用法:net_retry <单次预算秒> <最多尝试次数> <命令...>
#   每次尝试都由 timeout <预算> 包住;失败(含 timeout 的 124)打印一行诊断到
#   stderr,2 秒后重试;成功立刻返回 0;用尽次数返回最后一次的退出码;参数非法返回 2。
#
# 重试循环靠 rc,故本文件刻意不加 set -e。
set -uo pipefail

# 纯十进制正整数形态(空串、带单位、带符号都算非法:它们会让算术比较静默走偏)。
_net_retry_uint() {
    case "${1:-}" in
        ''|*[!0-9]*) return 1 ;;
    esac
    return 0
}

# 默认预算按用途分档(环境变量可覆盖)。拉取宽、导出中、摘要查询窄。
# 拉取的上限要能容下「慢但会成功」的那一类:2400s × 2 ≈ 最多 80 分钟,
# 仍远小于实机腿 180 分钟的 job 预算。
NET_PULL_TIMEOUT="${DAEDALUS_NET_PULL_TIMEOUT:-2400}"
NET_PULL_ATTEMPTS="${DAEDALUS_NET_PULL_ATTEMPTS:-2}"
NET_SAVE_TIMEOUT="${DAEDALUS_NET_SAVE_TIMEOUT:-900}"
NET_SAVE_ATTEMPTS="${DAEDALUS_NET_SAVE_ATTEMPTS:-2}"
NET_INSPECT_TIMEOUT="${DAEDALUS_NET_INSPECT_TIMEOUT:-60}"
NET_INSPECT_ATTEMPTS="${DAEDALUS_NET_INSPECT_ATTEMPTS:-3}"

# 只能给一次外层尝试的调用点用这个总上限(见 artifact-fingerprint.sh:那条路径
# 的既有自检把 podman pull 的调用次数钉死为恰好 1 次):单次预算 × 次数,
# 总时间上限与「多次尝试」等价。环境变量写坏时回落默认档 2400 × 2。
if _net_retry_uint "$NET_PULL_TIMEOUT" && _net_retry_uint "$NET_PULL_ATTEMPTS"; then
    NET_PULL_TOTAL_TIMEOUT=$(( NET_PULL_TIMEOUT * NET_PULL_ATTEMPTS ))
else
    NET_PULL_TOTAL_TIMEOUT=4800
fi

# 单次尝试。外部命令走 timeout —— 那是真正的硬预算。
# shell 函数没法被 timeout exec(函数不在 PATH 上,timeout 会去找同名的另一个
# 二进制,于是桩被绕过、真命令被执行),只有自检桩是这种形态:它们就地执行。
# 桩里没有会挂住的调用,预算对它没有意义。
_net_retry_once() {
    local budget="$1"
    shift
    case "$(type -t -- "${1:-}" 2>/dev/null)" in
        function|builtin) "$@" ;;
        *)                timeout "$budget" "$@" ;;
    esac
}

net_retry() {
    if [ "$#" -lt 3 ]; then
        printf 'net_retry: 用法: net_retry <单次预算秒> <最多尝试次数> <命令...>(收到 %d 个参数)\n' "$#" >&2
        return 2
    fi
    local budget="$1" attempts="$2"
    shift 2
    if ! _net_retry_uint "$budget" || [ "$budget" -lt 1 ]; then
        printf 'net_retry: 单次预算必须是正整数(收到 %s)\n' "$budget" >&2
        return 2
    fi
    if ! _net_retry_uint "$attempts" || [ "$attempts" -lt 1 ]; then
        printf 'net_retry: 尝试次数必须是正整数(收到 %s)\n' "$attempts" >&2
        return 2
    fi

    local n=1 rc=0
    while [ "$n" -le "$attempts" ]; do
        rc=0
        _net_retry_once "$budget" "$@" || rc=$?
        if [ "$rc" -eq 0 ]; then
            return 0
        fi
        if [ "$n" -ge "$attempts" ]; then
            printf 'net_retry: %s 连续失败 %d 次,放弃(最后 rc=%d)\n' "$*" "$attempts" "$rc" >&2
            return "$rc"
        fi
        printf 'net_retry: %s 第 %d 次失败(rc=%d),2 秒后重试\n' "$*" "$n" "$rc" >&2
        sleep 2
        n=$((n + 1))
    done
    return "$rc"
}
