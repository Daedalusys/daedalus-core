#!/usr/bin/env bash
# =============================================================================
# probe-first-run.sh —— 「装上之后头五分钟」验收探针(v0.1 门禁)
# =============================================================================
# 回答的是**用户视角**的问题:「拿到产物、开起来之后,我能看到什么、能做什么」。
# 与其它探针的分工:那些各证一个面(sandbox / audit / destructive / readonly /
# copilot / bootc);本探针把产品入口按用户会敲的顺序串一遍,并对「v0.1 能不能
# 让人上手」给出可判定的结论。
#
# 六段
#   T0 发布产物里有没有可登录账号(宿主侧对**发布镜像**取证,不进 guest)
#   T1 这台机器是谁(os-release / kernel / bootc 部署)
#   T2 能力面(daedalus-host list 根口径:14 插件全 ok)
#   T2b 镜像自带自检(daedalus-smoke)
#   T3 事务闭环(service.set:enabled → rollback)—— atomic 节点上的可回滚变更通道
#   T4 证据链(audit 哈希链 intact + 事务条目确实上链)
#   T5 package.set 在 immutable 节点上的行为(必须是**清晰 fail-closed**,不是 Python traceback)
#
# 为什么 T0 在宿主侧
#   guest-driver 派生的是一个**带随机口令的一次性镜像**,它不能证明发布镜像本身
#   可登录。T0 直接对 $VERIFY_IMAGE 取证,得到的才是「用户下载到的那份」的结论。
#
# 关于 sudo 的 PATH
#   镜像里 RHEL 默认 secure_path = /sbin:/bin:/usr/sbin:/usr/bin,**不含 /usr/local/bin**,
#   所以 `sudo daedalus-host` 必然 command not found。本探针一律用绝对路径调用
#   镜像内的二进制 —— 产品自己也是这么调的(wrapper 用绝对路径调宿主)。
#
# 用法(在 x86-64-v3 + KVM 宿主上,需 rootful podman 与 root)
#   sudo VERIFY_WORK=/tmp/probe-first-run \
#        VERIFY_IMAGE=ghcr.io/daedalusys/daedalus-os:latest \
#        bash tests/real-machine/probe-first-run.sh
# =============================================================================
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export VERIFY_WORK="${VERIFY_WORK:-/tmp/daedalus-probe-first-run}"
export DRIVER_LOG="$VERIFY_WORK/probe-first-run.log"
# shellcheck source=lib/guest-driver.sh
source "$HERE/lib/guest-driver.sh"
DRIVER_QEMU_SNAPSHOT=1

# 事务闭环用的 user-scope 单元(不是 daemon,只是让 systemctl --user 有个可开关的目标)
UNIT="daedalus-probe.service"

driver_init

# ---------------------------------------------------------------- T0 · 发布产物
say "T0 · 发布产物 $DRIVER_IMAGE 里有没有可登录账号(宿主侧,进 guest 之前)"
T0_PASSWD="$(podman run --rm "$DRIVER_IMAGE" getent passwd 2>&1 | awk -F: '$3>=1000 && $7!="/sbin/nologin" && $7!="/usr/sbin/nologin" {print}')"
T0_SHADOW="$(podman run --rm "$DRIVER_IMAGE" sh -c 'grep "^root:" /etc/shadow' 2>&1)"
printf '      非系统账号: %s\n' "${T0_PASSWD:-（无）}" | tee -a "$DRIVER_LOG"
printf '      root 口令字段: %s\n' "$T0_SHADOW" | tee -a "$DRIVER_LOG"
if [ -z "$T0_PASSWD" ]; then
    driver_verdict FAIL "T0 发布镜像无任何可登录账号(root 口令字段见上)→ 开起来只有 login: 提示符,人进不去"
else
    driver_verdict PASS "T0 发布镜像带可登录账号: $T0_PASSWD"
fi

driver_boot

# ------------------------------------------------------------------------- T1
say "T1 · 这台机器是谁"
T1_OS="$(gssh "cat /usr/lib/os-release" 2>&1)"
printf '%s\n' "$T1_OS" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
T1_KERNEL="$(gssh "uname -r" 2>&1)"
T1_STATUS="$(gssh "sudo /usr/bin/bootc status --json 2>&1 | head -c 600" 2>&1)"
printf '      bootc: %s\n' "$(printf '%s' "$T1_STATUS" | tr -d '\n' | head -c 200)" | tee -a "$DRIVER_LOG"
if printf '%s' "$T1_STATUS" | grep -q '"booted"'; then
    driver_verdict PASS "T1 系统身份可读(kernel=$T1_KERNEL,bootc 有 booted 部署)"
else
    driver_verdict FAIL "T1 bootc status 里没有 booted 部署: $(printf '%s' "$T1_STATUS" | head -1)"
fi

# ------------------------------------------------------------------------- T2
say "T2 · 能力面:daedalus-host list(根口径)"
T2_LIST="$(gssh "sudo /usr/local/bin/daedalus-host list" 2>&1)"
printf '%s\n' "$T2_LIST" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
T2_OK_N="$(printf '%s\n' "$T2_LIST" | grep -cE '[[:space:]]ok$')"
T2_BAD_N="$(printf '%s\n' "$T2_LIST" | grep -c 'degraded')"
if [ "$T2_OK_N" -ge 14 ] && [ "$T2_BAD_N" = 0 ]; then
    driver_verdict PASS "T2 14 个插件全部 ok(实测 ok=$T2_OK_N degraded=$T2_BAD_N)"
else
    driver_verdict FAIL "T2 插件面不完整(ok=$T2_OK_N degraded=$T2_BAD_N,期望 ok>=14 degraded=0)"
fi

# ------------------------------------------------------------------------ T2b
say "T2b · 镜像自带自检 daedalus-smoke"
T2B="$(gssh "sudo /usr/local/bin/daedalus-smoke 2>&1; echo RC=\$?" 2>&1)"
printf '%s\n' "$T2B" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
if printf '%s' "$T2B" | grep -q 'RC=0'; then
    driver_verdict PASS "T2b daedalus-smoke 通过"
else
    driver_verdict FAIL "T2b daedalus-smoke 非零退出"
fi

# ------------------------------------------------------------------------- T3
say "T3 · 事务闭环:service.set started → rollback(user-scope)"
# 用 started↔stopped 这一对判闭环:它是适配器真正能逆的一对
# (serviceSetInverseVerbs: active→start / inactive→stop)。
# user-scope 事务要 user manager 在跑。探针从 root 经 SSH 进来,不是 login session,
# pam_systemd 不会替 verify 建 /run/user/<uid> —— 必须显式 enable-linger 并把
# user@<uid> 拉起来,再等 /run/user/<uid>/bus 出现。
# (2026-11-19 出货构建里这一段没备好,`systemctl --user` 报 "Failed to connect to user
#  scope bus",T3 因此判 FAIL —— 那是探针环境没备好,不是产品缺陷。)
UID_V="$(gssh "id -u verify" 2>/dev/null | tr -d '\r')"
if [ -z "$UID_V" ]; then
    driver_verdict FAIL "T3 取不到 verify 的 uid"
    driver_exit
fi
ok "verify uid=$UID_V"
gssh "sudo loginctl enable-linger verify >/dev/null 2>&1; sudo systemctl start user@$UID_V.service >/dev/null 2>&1" >/dev/null 2>&1
T3_BUS=0
for _i in $(seq 1 20); do
    if gssh "test -S /run/user/$UID_V/bus" >/dev/null 2>&1; then T3_BUS=1; break; fi
    sleep 2
done
if [ "$T3_BUS" != "1" ]; then
    driver_verdict FAIL "T3 user manager 没起来(/run/user/$UID_V/bus 不存在),环境不备"
    driver_exit
fi
gssh "sudo -iu verify bash -lc 'export XDG_RUNTIME_DIR=/run/user/$UID_V; mkdir -p ~/.config/systemd/user; printf \"[Unit]\\nDescription=probe\\n[Service]\\nExecStart=/bin/sleep 300\\n[Install]\\nWantedBy=default.target\\n\" > ~/.config/systemd/user/$UNIT; systemctl --user daemon-reload; systemctl --user stop $UNIT'" >/dev/null 2>&1
T3_TX="$(gssh "sudo -iu verify bash -lc 'export XDG_RUNTIME_DIR=/run/user/$UID_V; /usr/local/bin/daedalus-tx begin'" 2>/dev/null | sed -n 's/.*"tx_id":"\([a-f0-9]\{16\}\).*/\1/p')"
if [ -z "$T3_TX" ]; then
    driver_verdict FAIL "T3 begin 没给出 tx_id"
else
    ok "tx_id=$T3_TX"
    T3_APPLY="$(gssh "sudo -iu verify bash -lc 'export XDG_RUNTIME_DIR=/run/user/$UID_V; /usr/local/bin/daedalus-tx propose $T3_TX service.set \"{\\\"name\\\":\\\"$UNIT\\\",\\\"desired_state\\\":\\\"started\\\"}\" >/dev/null 2>&1; /usr/local/bin/daedalus-tx apply $T3_TX; echo APPLY_RC=\$?'" 2>&1)"
    T3_ACTIVE="$(gssh "sudo -iu verify bash -lc 'export XDG_RUNTIME_DIR=/run/user/$UID_V; systemctl --user is-active $UNIT'" 2>&1)"
    T3_RB="$(gssh "sudo -iu verify bash -lc 'export XDG_RUNTIME_DIR=/run/user/$UID_V; /usr/local/bin/daedalus-tx rollback $T3_TX; echo RB_RC=\$?'" 2>&1)"
    T3_AFTER="$(gssh "sudo -iu verify bash -lc 'export XDG_RUNTIME_DIR=/run/user/$UID_V; systemctl --user is-active $UNIT'" 2>&1)"
    printf '      is-active(apply 后): %s\n' "$T3_ACTIVE" | tee -a "$DRIVER_LOG"
    printf '      is-active(回滚后): %s\n' "$T3_AFTER" | tee -a "$DRIVER_LOG"
    if printf '%s' "$T3_APPLY" | grep -q 'APPLY_RC=0' \
       && printf '%s' "$T3_ACTIVE" | grep -q '^active' \
       && printf '%s' "$T3_RB" | grep -q 'RB_RC=0' \
       && printf '%s' "$T3_AFTER" | grep -q 'inactive'; then
        driver_verdict PASS "T3 $UNIT started → rollback 回 inactive,事务闭环成立"
    else
        driver_verdict FAIL "T3 事务闭环不成立(见上;apply/rb 原文在 $DRIVER_LOG)"
    fi
fi

# ---- T3b · enabled 维度的回滚 ------------------------------------------------
# enabled 与运行态是两个正交维度: `desired_state=enabled` 只动 enable 符号链接
# (`~/.config/systemd/user/*.wants/`),不动 ActiveState。回滚必须按 Propose 期
# `is-enabled` 的观测值恢复该维度 —— 旧实现只按 active_state 发逆动词,单元原本
# inactive 时发的是 stop(空转),符号链接留在原地,而事务 status 仍报 rolled_back
# (Daedalusys/daedalus-core#195,已修)。
# 判据是「回滚前后 is-enabled 完全一致」,不是硬编码某个值 —— 探针不假设起始态。
say "T3b · enabled 维度的回滚(#195)"
T3B_BEFORE="$(gssh "sudo -iu verify bash -lc 'export XDG_RUNTIME_DIR=/run/user/$UID_V; systemctl --user is-enabled $UNIT'" 2>&1 | tr -d '\r')"
printf '      is-enabled(回滚前): %s\n' "$T3B_BEFORE" | tee -a "$DRIVER_LOG"
T3B_TX="$(gssh "sudo -iu verify bash -lc 'export XDG_RUNTIME_DIR=/run/user/$UID_V; /usr/local/bin/daedalus-tx begin'" 2>/dev/null | sed -n 's/.*"tx_id":"\([a-f0-9]\{16\}\).*/\1/p')"
if [ -z "$T3B_TX" ]; then
    driver_verdict FAIL "T3b begin 没给出 tx_id"
elif [ -z "$T3B_BEFORE" ]; then
    driver_verdict FAIL "T3b 回滚前 is-enabled 观测不到,无法判定 enabled 维度"
else
    # apply(enabled) → 观测: 必须是 enabled(证明这一步确实动了 enabled 维度)
    gssh "sudo -iu verify bash -lc 'export XDG_RUNTIME_DIR=/run/user/$UID_V; /usr/local/bin/daedalus-tx propose $T3B_TX service.set \"{\\\"name\\\":\\\"$UNIT\\\",\\\"desired_state\\\":\\\"enabled\\\"}\" >/dev/null 2>&1; /usr/local/bin/daedalus-tx apply $T3B_TX >/dev/null 2>&1'" >/dev/null 2>&1
    T3B_APPLIED="$(gssh "sudo -iu verify bash -lc 'export XDG_RUNTIME_DIR=/run/user/$UID_V; systemctl --user is-enabled $UNIT'" 2>&1 | tr -d '\r')"
    printf '      is-enabled(apply 后): %s\n' "$T3B_APPLIED" | tee -a "$DRIVER_LOG"
    # rollback → 观测: 必须回到回滚前的值(符号链接被摘掉)
    gssh "sudo -iu verify bash -lc 'export XDG_RUNTIME_DIR=/run/user/$UID_V; /usr/local/bin/daedalus-tx rollback $T3B_TX >/dev/null 2>&1'" >/dev/null 2>&1
    T3B_AFTER="$(gssh "sudo -iu verify bash -lc 'export XDG_RUNTIME_DIR=/run/user/$UID_V; systemctl --user is-enabled $UNIT'" 2>&1 | tr -d '\r')"
    printf '      is-enabled(rollback 后): %s\n' "$T3B_AFTER" | tee -a "$DRIVER_LOG"
    if [ "$T3B_APPLIED" != "enabled" ]; then
        driver_verdict FAIL "T3b apply(enabled) 后 is-enabled 应为 enabled,实得 $T3B_APPLIED"
    elif [ "$T3B_AFTER" = "$T3B_BEFORE" ]; then
        driver_verdict PASS "T3b enabled 维度回滚成立(回滚前后都是 $T3B_BEFORE)"
    else
        driver_verdict FAIL "T3b enabled 维度回滚不掉(回滚前 $T3B_BEFORE → 回滚后 $T3B_AFTER):#195 回归"
    fi
fi

# ------------------------------------------------------------------------- T4
say "T4 · 证据链"
T4_VERIFY="$(gssh "sudo /usr/local/bin/daedalus-audit verify --log-path /var/log/daedalus/audit.jsonl 2>&1")"
printf '      %s\n' "$T4_VERIFY" | tee -a "$DRIVER_LOG"
T4_LAST="$(gssh "sudo tail -2 /var/log/daedalus/audit.jsonl 2>&1" | head -c 1200)"
printf '%s\n' "$T4_LAST" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
if printf '%s' "$T4_VERIFY" | grep -qi 'hash chain intact'; then
    driver_verdict PASS "T4 审计链 intact($T4_VERIFY)"
else
    driver_verdict FAIL "T4 审计链校验未通过:$T4_VERIFY"
fi

# ------------------------------------------------------------------------- T5
# package kind 在 immutable(bootc)节点上**按设计**不可用:atomic 节点的包变更走
# bootc upgrade(新镜像 + 可回滚),package kind 只适用于 agent 节点(普通发行版)。
# 拒绝发生在 propose(而不是 dnf 真的跑起来之后),所以这里验的是 propose 的输出:
# 它必须是清晰边界文案,不是 Python traceback,也不是「先跑了 dnf 再报错」。
say "T5 · package.set 在 immutable 节点上的失败形态(propose 即拒)"
T5="$(gssh "T=\$(sudo /usr/local/bin/daedalus-tx begin 2>/dev/null | sed -n 's/.*\"tx_id\":\"\([a-f0-9]\{16\}\)\".*/\1/p'); sudo /usr/local/bin/daedalus-tx propose \$T package.set '{\"name\":\"zsh\",\"desired_state\":\"present\"}' 2>&1 | head -c 700" 2>&1)"
printf '%s\n' "$T5" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
if printf '%s' "$T5" | grep -qi 'Traceback'; then
    driver_verdict FAIL "T5 package.set 在 immutable 节点上抛的是 Python traceback,不是清晰拒绝"
elif printf '%s' "$T5" | grep -qiE 'immutable|bootc|不支持|not supported|agent'; then
    driver_verdict PASS "T5 package.set 在 immutable 节点上 propose 即清晰 fail-closed"
else
    driver_verdict NOTE "T5 package.set 的失败文案既非 traceback 也不含明确边界词,原文见上"
fi

driver_exit
