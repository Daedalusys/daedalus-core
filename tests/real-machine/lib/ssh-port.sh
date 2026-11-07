#!/usr/bin/env bash
# KVM 门禁的 SSH hostfwd 端口解析与占用探测。
#
# 端口冲突与「产物引导失败」是两件事:前者 qemu 起不来、退到 QEMU_EXITED,
# 会让查的人先怀疑 qcow2 坏了。所以冲突必须在起 qemu 之前显式报出来。

# ssh_port_resolve <default> —— 解析最终端口;非法值 fail-fast 而不是静默回落。
ssh_port_resolve() {
    local default="$1" raw="${VERIFY_SSH_PORT:-}"
    if [ -z "$raw" ]; then
        printf '%s\n' "$default"
        return 0
    fi
    if ! [[ "$raw" =~ ^[0-9]+$ ]] || [ "$raw" -lt 1 ] || [ "$raw" -gt 65535 ]; then
        echo "ERROR: VERIFY_SSH_PORT='$raw' 非法(须为 1..65535 的整数)" >&2
        return 2
    fi
    printf '%s\n' "$raw"
}

# ssh_port_guard <port> —— 起 qemu 前探测端口是否已被占用。
# 被占 → 打印占用者(ss 原文)并返回 3;探测能力缺失 → WARN 后返回 0
# (降级而非静默跳过:日志里必须留下"这次没做冲突检测")。
ssh_port_guard() {
    local port="$1" hit
    if ! command -v ss >/dev/null 2>&1; then
        echo "WARN: 找不到 ss,跳过 hostfwd 端口 $port 的占用探测(若 qemu 起不来请先看端口)" >&2
        return 0
    fi
    hit="$(ss -ltnp "sport = :$port" 2>/dev/null | sed -n '2,$p')"
    if [ -n "$hit" ]; then
        echo "ERROR: hostfwd 端口 $port 已被占用,本门禁未启动 qemu(这不是产物问题)" >&2
        echo "       占用者: $hit" >&2
        echo "       处置: 释放该端口,或设 VERIFY_SSH_PORT=<空闲端口> 重跑" >&2
        return 3
    fi
    return 0
}
