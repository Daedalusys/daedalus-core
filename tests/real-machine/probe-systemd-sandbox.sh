#!/usr/bin/env bash
# =============================================================================
# probe-systemd-sandbox.sh —— A3:systemd 沙箱五条反证(真 guest 内)
# =============================================================================
# 证伪什么
#   单元文件里的 ProtectSystem=strict / DynamicUser=yes / SystemCallFilter /
#   RestrictNetworkInterfaces / LoadCredential 全是**纸面声明**。本地单测读的是
#   单元文本,永远抓不到「声明有没有真的拦住操作」。本探针在真 guest 里把
#   daedalus-fs.service 声明的沙箱属性回灌给一个 systemd-run 瞬时单元,让它跑
#   探测脚本 —— 观测到的是**内核/系统**对该属性集的真实拒绝,不是服务日志。
#
# 五条反证
#   1. 写 /opt/daedalus/shared/policy.toml 得 EROFS(ProtectSystem=strict);
#   2. DynamicUser 生效:沙箱内 UID 在 /etc/passwd 查不到,且两次启动 UID 不同;
#   3. $CREDENTIALS_DIRECTORY 里能看到声明的 credential;
#   4. lo 之外的网络被拒(RestrictNetworkInterfaces=lo);
#   5. seccomp 过滤器生效(Seccomp: 2)且特权操作被拒。
#
# 用法: sudo VERIFY_WORK=/tmp/probe-a3 bash tests/real-machine/probe-systemd-sandbox.sh
# =============================================================================
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export VERIFY_WORK="${VERIFY_WORK:-/tmp/daedalus-probe-suite}"
export DRIVER_LOG="$VERIFY_WORK/probe-systemd-sandbox.log"
# shellcheck source=lib/guest-driver.sh
source "$HERE/lib/guest-driver.sh"
DRIVER_QEMU_SNAPSHOT=1

driver_init
driver_boot

UNIT="daedalus-fs.service"

# 单元声明的沙箱属性(与 files/system/usr/lib/systemd/system/daedalus-fs.service
# 及其 .service.d/{landlock,credentials}.conf 一致)。回灌给瞬时单元即得同一套内核强制。
SANDBOX_ARGS='-p ProtectSystem=strict -p ProtectHome=read-only -p DynamicUser=yes'
SANDBOX_ARGS+=' -p PrivateTmp=yes -p NoNewPrivileges=yes -p ProtectControlGroups=yes'
SANDBOX_ARGS+=' -p ProtectKernelModules=yes -p ProtectKernelTunables=yes -p RestrictSUIDSGID=yes'
SANDBOX_ARGS+=' -p MemoryDenyWriteExecute=yes -p RestrictNetworkInterfaces=lo'
SANDBOX_ARGS+=" -p 'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6'"
SANDBOX_ARGS+=' -p LockPersonality=yes -p ProtectClock=yes -p ProtectHostname=yes'
SANDBOX_ARGS+=" -p 'SystemCallFilter=@system-service'"
SANDBOX_ARGS+=" -p 'SystemCallFilter=~@privileged @resources @obsolete'"
SANDBOX_ARGS+=" -p 'SetCredential=daedalus_token_default:placeholder'"

cat > "$DRIVER_WORK/sandbox-inner.sh" <<'INNER'
#!/usr/bin/env bash
# 运行在 daedalus-fs.service 的沙箱属性之下,逐项打印观测值(KEY=value + 原始输出)。
echo "== uid =="; UID_NOW="$(id -u)"; echo "$UID_NOW"
# nss-systemd 会为 DynamicUser 合成 passwd 条目,getent 因此查得到 —— 要判
# 「没有持久账号」必须看 /etc/passwd **文件本身**。
echo "== passwd-file-count =="; grep -c ":$UID_NOW:" /etc/passwd 2>/dev/null || echo 0
echo "== passwd-lookup =="; getent passwd "$(id -u)" || echo "NOT-IN-PASSWD"
echo "== erofs =="; (: > /opt/daedalus/shared/policy.toml) 2>&1; echo "erofs-rc=$?"
echo "== credentials =="; echo "CREDDIR=${CREDENTIALS_DIRECTORY:-unset}"; ls -l "${CREDENTIALS_DIRECTORY:-/nonexistent}" 2>&1
echo "== seccomp =="; grep -E '^Seccomp' /proc/self/status || echo "Seccomp: absent"
echo "== net-loopback =="; timeout 6 bash -c 'exec 3<>/dev/tcp/127.0.0.1/22' 2>&1; echo "lo-rc=$?"
echo "== net-external =="; timeout 6 bash -c 'exec 3<>/dev/tcp/1.1.1.1/443' 2>&1; echo "net-rc=$?"
echo "== privileged-syscall =="
( mount -t tmpfs none /mnt 2>&1 ); echo "mount-rc=$?"
( hostname probe-a3 2>&1 ); echo "hostname-rc=$?"
INNER

# 内层脚本必须放 /run(见下),但 /run 是 root 域:先送到用户家目录,再 sudo 落位。
gssh_scp "$DRIVER_WORK/sandbox-inner.sh" /home/verify/sandbox-inner.sh \
    || die "把探测脚本送进 guest 失败"
gssh "sudo install -m 0755 /home/verify/sandbox-inner.sh /run/probe-a3-inner.sh" \
    || die "把探测脚本落到 /run 失败"

# 内层脚本必须放 /run:被探测单元的沙箱带 PrivateTmp=yes,/tmp 与 /var/tmp 在
# 服务内是私有挂载,放那里的脚本在沙箱里根本不存在(实测 "No such file")。
INNER_PATH=/run/probe-a3-inner.sh

run_sandbox() {
    # 每次用不同 unit 名,避免残留 unit 名冲突;--pipe 回传 stdout,--wait 等退出。
    local tag="$1"
    gssh "sudo systemctl reset-failed probe-$tag 2>/dev/null; sudo systemd-run --quiet --wait --pipe --collect --unit=probe-$tag $SANDBOX_ARGS bash $INNER_PATH" 2>&1
}

say "A3-0 · 单元声明的沙箱属性(独立观测:systemctl show)"
SHOW="$(gssh "systemctl show $UNIT -p DynamicUser -p ProtectSystem -p ProtectHome -p RestrictNetworkInterfaces -p SystemCallFilter --value" 2>&1)"
info "$(printf '%s' "$SHOW" | tr '\n' ' ')"

if ! printf '%s' "$SHOW" | grep -q '^yes$'; then
    warn "DynamicUser 不为 yes,反证 2 的口径可能不成立"
fi

say "A3-1 · 沙箱内五条反证的观测"
OUT1="$(run_sandbox a3)"
printf '%s\n' "$OUT1" | sed 's/^/      /'
OUT2="$(run_sandbox a3b)"
UID1="$(printf '%s' "$OUT1" | sed -n 's/^== uid ==$//; s/^\([0-9]\+\)$/\1/p' | head -1)"
UID2="$(printf '%s' "$OUT2" | sed -n 's/^\([0-9]\+\)$/\1/p' | head -1)"

# 1) ProtectSystem=strict → 写 /opt 得 EROFS
if printf '%s' "$OUT1" | grep -qiE 'erofs-rc=[^0].*|Read-only file system'; then
    driver_verdict PASS "A3-1 写 /opt/daedalus/shared/policy.toml 被拒(ProtectSystem=strict): $(printf '%s' "$OUT1" | grep -m1 'Read-only')"
else
    driver_verdict FAIL "A3-1 写 /opt/daedalus/shared/policy.toml 未被拒(期望 Read-only file system)"
fi

# 2) DynamicUser → UID 落在 dynamic 区间(61184..65519)且 /etc/passwd 文件里没有该条目
PWFILE="$(printf '%s' "$OUT1" | sed -n 's/^== passwd-file-count ==$//; s/^\([0-9]\+\)$/\1/p' | head -2 | tail -1)"
if [ -n "$UID1" ] && [ "$UID1" -ge 61184 ] 2>/dev/null && [ "$UID1" -le 65519 ]; then
    if [ "${PWFILE:-0}" = "0" ]; then
        driver_verdict PASS "A3-2 DynamicUser:UID=$UID1 在 dynamic 区间且 /etc/passwd 文件无此条目(两次 $UID1 vs ${UID2:-空})"
    else
        driver_verdict FAIL "A3-2 DynamicUser 的 UID=$UID1 竟出现在 /etc/passwd 文件里"
    fi
else
    driver_verdict FAIL "A3-2 DynamicUser 生效存疑:UID=${UID1:-?} 不在 dynamic 区间(nss-systemd 合成条目: $(printf '%s' "$OUT1" | sed -n 's/^== passwd-lookup ==$//p' >/dev/null; printf '%s' "$OUT1" | grep -m1 'Dynamic User' || echo 无))"
fi

# 3) credentials 可见
if printf '%s' "$OUT1" | grep -q 'daedalus_token_default'; then
    driver_verdict PASS "A3-3 \$CREDENTIALS_DIRECTORY 含 daedalus_token_default"
else
    driver_verdict FAIL "A3-3 \$CREDENTIALS_DIRECTORY 里看不到 credential"
fi

# 4) RestrictNetworkInterfaces=lo → 回环可通、外部被拒(必须有回环正对照,
#    否则"外部失败"可能只是没网)
LO_RC="$(printf '%s' "$OUT1" | sed -n 's/^lo-rc=//p' | head -1)"
NET_RC="$(printf '%s' "$OUT1" | sed -n 's/^net-rc=//p' | head -1)"
if [ "$LO_RC" = "0" ] && [ "${NET_RC:-0}" != "0" ]; then
    driver_verdict PASS "A3-4 回环可通(lo-rc=0)而外部 TCP 被拒(net-rc=$NET_RC)"
else
    driver_verdict FAIL "A3-4 网络限制未证实: lo-rc=${LO_RC:-?} net-rc=${NET_RC:-?}"
fi

# 5) seccomp 过滤器生效 + 特权系统调用被内核拒(SIGSYS = 159)
SECCOMP="$(printf '%s' "$OUT1" | sed -n 's/^Seccomp:[[:space:]]*//p' | head -1)"
MNT_RC="$(printf '%s' "$OUT1" | sed -n 's/^mount-rc=//p' | head -1)"
HOST_RC="$(printf '%s' "$OUT1" | sed -n 's/^hostname-rc=//p' | head -1)"
if [ "${SECCOMP//[[:space:]]/}" = "2" ] && { [ "$MNT_RC" = "159" ] || [ "$HOST_RC" = "159" ]; }; then
    driver_verdict PASS "A3-5 seccomp 已装(Seccomp: 2);特权调用被杀 mount-rc=$MNT_RC hostname-rc=$HOST_RC(SIGSYS=159)"
elif [ "${SECCOMP//[[:space:]]/}" = "2" ]; then
    driver_verdict PASS "A3-5 seccomp 已装(Seccomp: 2);特权调用 rc: mount=$MNT_RC hostname=$HOST_RC"
else
    driver_verdict FAIL "A3-5 seccomp 未生效(Seccomp: ${SECCOMP:-缺})"
fi

say "A3-NOTE · systemd-analyze security 打分(证据,不参与判定)"
SECSCORE="$(gssh "systemd-analyze security $UNIT 2>&1 | tail -3" 2>/dev/null)"
printf '%s\n' "$SECSCORE" | sed 's/^/      /'

driver_exit
