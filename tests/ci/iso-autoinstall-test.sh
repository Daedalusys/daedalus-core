#!/usr/bin/env bash
# 无人值守 ISO 装机的**静态**门禁。它不碰靶机、不需要 KVM,只对仓库里的产物做断言,
# 所以要跑得快、跑得早:真正烧靶机时间的 P2-2a/P2-2b 由 iso-verify.sh 在真机跑。
#
# 断言的不变式(每一条都对应一次实测踩过的坑或一条安全底线):
#   规则1 live 环境装机脚本存在、可执行,且**只按内核参数**确定目标盘
#        (任何"自动挑第一块盘"的写法都是把宿主真盘当靶子的前身)
#   规则2 目标盘必须是整盘:脚本里要有 lsblk TYPE=disk 与 live 载体排除两道闸
#   规则3 脚本里不得出现明文口令 / 私钥材料(口令随机生成、只记指纹)
#   规则4 脚本不得出现 losetup / dd 到块设备的宿主盘操作
#   规则5 系统单元只在 live 介质上触发(否则装好的系统会无限重装)
#   规则6 iso-verify.sh 仍走 generic-iso,且不再依赖 blueprint/kickstart
#        (Task 1 实测:generic-iso 不消费 blueprint,那条路是死的)
#   规则7 iso-verify.sh 每次新建目标盘 qcow2,并以 hostfwd 2222 做 live 自证
#   规则8 Task 2 要求的装机成功独立证据齐备:串口完成行 + 装完后目标盘分区布局断言
#   规则9 段二(P2-2b)是**默认硬判据**,且出货档 workflow 显式写死(不得默默可选)
#
# 用法:
#   iso-autoinstall-test.sh          # 静态断言,全绿打印 RESULT: SELFTEST PASS
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
INST="$REPO/tests/real-machine/iso-autoinstall.sh"
UNIT="$REPO/tests/real-machine/iso-autoinstall.service"
VERIFY="$REPO/tests/real-machine/iso-verify.sh"

fail_n=0
pass_n=0
st() { # st <条件结果 0/1> <说明>
    if [ "$1" -eq 0 ]; then
        pass_n=$((pass_n + 1)); printf 'PASS %s\n' "$2"
    else
        fail_n=$((fail_n + 1)); printf 'FAIL %s\n' "$2"
    fi
}
has() { grep -qF -- "$1" "$2" 2>/dev/null; }
hasre() { grep -qE -- "$1" "$2" 2>/dev/null; }

# ---------------------------------------------------------- 规则1/2/3/4:装机脚本 --
if [ -f "$INST" ]; then
    st 0 "规则1 装机脚本存在:$INST"
    [ -x "$INST" ]; st $? "规则1 装机脚本带可执行位"
    has 'daedalus.install.disk=' "$INST"; st $? "规则1 装机脚本从内核参数取目标盘"
    hasre 'die .*拒绝安装|拒绝安装' "$INST"; st $? "规则1 未给目标盘时 fail-closed 拒绝执行"
    # 反向断言:不得出现"自动挑盘"的写法
    if hasre 'lsblk[^|]*\|[[:space:]]*head|/dev/sda|/dev/nvme0n1|first disk' "$INST"; then
        st 1 "规则1 装机脚本不得自动挑第一块盘(发现猜测式选盘)"
    else
        st 0 "规则1 装机脚本不做任何自动选盘"
    fi
    hasre 'lsblk -ndo TYPE|lsblk.*TYPE' "$INST"; st $? "规则2 目标盘要求 lsblk TYPE=disk(整盘)"
    hasre 'run/initramfs/live|live 介质' "$INST"; st $? "规则2 排除 live 介质自身所在设备"
    # 明文凭据:短语匹配(占位符/变量名不算)
    if hasre 'password=[^$"{]|rootpw[[:space:]]+--plaintext|BEGIN [A-Z ]*PRIVATE KEY' "$INST" "$UNIT"; then
        st 1 "规则3 产物里不得出现明文口令或私钥材料"
    else
        st 0 "规则3 产物里没有明文口令 / 私钥材料"
    fi
    if hasre 'losetup|dd .*of=/dev/(sd|vd|nvme)' "$INST"; then
        st 1 "规则4 不得出现 losetup / dd 到块设备的操作"
    else
        st 0 "规则4 没有宿主块设备直写操作"
    fi
    hasre 'bootc install to-disk' "$INST"; st $? "规则1 装机动作是 bootc install to-disk"
    has '--wipe' "$INST"; st $? "规则1 显式 --wipe(整盘清空是意图明确的动作)"
    has '--source-imgref' "$INST"; st $? "规则1 显式给出 --source-imgref(live 环境无隐式来源)"
    hasre 'SRC_IMGREF="registry:|registry:' "$INST"
    st $? "规则1 source-imgref 带显式 transport 前缀(bootc 拒裸 ref)"
    # 规则10:安装源可以与目标分离(加速源),但三条底线不能破:
    #   (a) 源覆盖项必须被读出来并用于 --source-imgref
    #   (b) --target-imgref 必须始终是规范 ref —— 它决定装好系统以后的更新通道,
    #       指向加速镜像等于把更新权交给第三方
    #   (c) 单个源要有时限:加速源不通时要能回落,而不是把整条门禁的时间预算耗光
    has 'DAEDALUS_INSTALL_SOURCE_IMGREF' "$INST"
    st $? "规则10 装机脚本读安装源覆盖项(加速源)"
    hasre '--source-imgref "\$SRC_IMGREF"' "$INST"
    st $? "规则10 --source-imgref 用的是源 ref"
    hasre '--target-imgref "\$IMGREF"' "$INST"
    st $? "规则10 --target-imgref 用的是规范 ref(更新通道不交给加速镜像)"
    if hasre '--target-imgref "\$SRC' "$INST"; then
        st 1 "规则10 --target-imgref 不得指向加速源"
    else
        st 0 "规则10 --target-imgref 不指向加速源"
    fi
    hasre 'timeout .*bootc install to-disk' "$INST"
    st $? "规则10 单个安装源有独立时限(加速源不通可回落)"
else
    st 1 "规则1 装机脚本存在:$INST"
fi

# -------------------------------------------------------------- 规则5:系统单元 --
if [ -f "$UNIT" ]; then
    st 0 "规则5 单元文件存在:$UNIT"
    hasre 'ConditionKernelCommandLine=.*rd\.live\.image' "$UNIT"
    st $? "规则5 单元只在 live 介质触发(装好的系统不具备 rd.live.image)"
    has 'WantedBy=multi-user.target' "$UNIT"; st $? "规则5 单元挂在 multi-user.target"
else
    st 1 "规则5 单元文件存在:$UNIT"
fi

# ---------------------------------------------------------- 规则6/7/8:iso-verify --
if [ -f "$VERIFY" ]; then
    hasre 'generic-iso' "$VERIFY"; st $? "规则6 iso-verify.sh 仍用 generic-iso"
    # 反向断言:不把 blueprint 真传下去、不再引用 kickstart 模板。
    # (脚本头部会解释为什么不走这条路,故注释里出现该词不算违规,这里查的是**用法**。)
    if hasre -- '--blueprint|iso-kickstart' "$VERIFY"; then
        st 1 "规则6 iso-verify.sh 不得再依赖 blueprint/kickstart(generic-iso 不消费 blueprint)"
    else
        st 0 "规则6 iso-verify.sh 不再把 blueprint / kickstart 当真用"
    fi
    has '2026-11-06-iso-blueprint.md' "$VERIFY"
    st $? "规则6 脚本里锚定了 Task 1 探查笔记(避免有人再走回头路)"
    hasre 'SSH_PORT="\$\{VERIFY_SSH_PORT:-2222\}"' "$VERIFY"
    st $? "规则7 live 自证端口可配(默认 2222;并行 worker 用 VERIFY_SSH_PORT 避让)"
    hasre 'hostfwd=tcp::.*-:22' "$VERIFY"; st $? "规则7 live 自证走 hostfwd"
    hasre 'qemu-img create' "$VERIFY"; st $? "规则7 每次新建目标盘 qcow2(不复用旧盘)"
    hasre 'daedalus\.install\.disk=/dev/vda' "$VERIFY"; st $? "规则7 目标盘通过内核参数点名"
    # 上游 iso.yaml 的字段名是 kernel_args;写成 kernelargs 会被静默忽略(实测踩过)
    has 'kernel_args:' "$VERIFY"; st $? "规则7 iso.yaml 用上游的 kernel_args 字段名"
    hasre 'live-cmdline|daedalus\.install\.disk=' "$VERIFY"
    st $? "规则7 运行时校验内核参数是否真的带上了目标盘"
    has 'login:' "$VERIFY"; st $? "规则8 判据含装好后系统的 login:"
    hasre 'fdisk -l|sfdisk -d|parted -s' "$VERIFY"; st $? "规则8 独立断言目标盘有分区表"
    hasre 'INSTALLED_LOGIN|LIVE_OK' "$VERIFY"; st $? "规则8 判定态含 live 自证与装机完成两级"
    hasre 'findmnt' "$VERIFY"; st $? "规则7 live 自证断言根是 live 介质"
    # 规则9:段二必须是**默认硬判据**,而且出货档 workflow 也要显式写死 ——
    # 否则装机链(单元 / kernel args / dmsquash-live)任何一处退化都会被 LIVE_OK 掩盖成绿。
    hasre 'VERIFY_REQUIRE_INSTALL:-1' "$VERIFY"; st $? "规则9 脚本默认要求装完能引导(不是可选)"
    # 规则11:加速源是**传输通道**,不是信任锚 —— 写进 payload 的源 ref 必须按 digest 钉死
    # (containers/image 逐层校验),否则加速镜像就能决定我们装进去的是什么。
    hasre 'VERIFY_INSTALL_MIRROR' "$VERIFY"; st $? "规则11 加速源可配(VERIFY_INSTALL_MIRROR)"
    hasre 'INSTALL_SOURCE_REF=.*@' "$VERIFY"
    st $? "规则11 写进 payload 的加速源按 digest 钉死(@sha256:...)"
    if [ -f "$REPO/.github/workflows/build.yml" ]; then
        hasre 'VERIFY_INSTALL_MIRROR' "$REPO/.github/workflows/build.yml"
        st $? "规则11 出货档 workflow 显式写明加速源(可审)"
    else
        st 1 "规则11 出货档 workflow 存在:$REPO/.github/workflows/build.yml"
    fi
    if [ -f "$REPO/.github/workflows/build.yml" ]; then
        hasre 'VERIFY_REQUIRE_INSTALL: "?1"?' "$REPO/.github/workflows/build.yml"
        st $? "规则9 出货档 workflow 显式设 VERIFY_REQUIRE_INSTALL: 1"
    else
        st 1 "规则9 出货档 workflow 存在:$REPO/.github/workflows/build.yml"
    fi
else
    st 1 "规则6 iso-verify.sh 存在:$VERIFY"
fi

echo
printf -- '---------------- %d 条通过 / %d 条失败 ----------------\n' "$pass_n" "$fail_n"
if [ "$fail_n" -eq 0 ]; then
    echo "RESULT: SELFTEST PASS"
    exit 0
fi
echo "RESULT: SELFTEST FAIL ($fail_n)"
exit 1
