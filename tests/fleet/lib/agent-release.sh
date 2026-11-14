#!/usr/bin/env bash
#
# 靶场取件层:从 GitHub Release 取 daedalus-agent 出货包,并断言被控端装出来的
# 版本就是这个 tag。被 tests/fleet/agent-e2e.sh source;也可直接执行跑自检。
#
# 为什么要有这一层:agent 包曾经靠"开发机 go build + tar + scp 到各台机器"分发,
# 产物与脚本都散在机器上,装错版本也看不出来 —— 裸构建自报 `0.1.0-dev`,与发布版
# 长得像亲戚。取件只留一条正路(release 资产 + SHA256SUMS 比对),本地 tarball
# 降级为开发态旁路;装完再用二进制自报的 `version` 反证:能拼出 `commit=` 才说明
# 机器上跑的是带 ldflags 的发布产物。
#
# 三个函数:
#   agent_asset_name <version> <arch>     → daedalus-agent_<version>_linux_<arch>.tar.gz
#   fetch_agent_release <version> <dir>   → 下载 + 校验和比对,stdout 只打印本地
#                                           tarball 的绝对路径(可直接进 $(...))
#   assert_agent_version <ssh别名> <版本>  → 目标机上跑 version,输出须含 "<版本> commit="
#
# 版本号的两种写法在这里抹平:资产名用**不带 v** 的版本(`0.1.0`),release 查找用
# **带 v** 的 tag(`v0.1.0`)。调用方两种都能传,不必自己剥前缀。
#
# 用法: bash tests/fleet/lib/agent-release.sh --selftest
#
# 退出码:0 通过;1 断言/取件失败;2 用法错误。

# release 仓、命令名、目标机二进制路径都可覆写:自检靠 PATH 前置的假 gh/ssh 驱动,
# 真机靠真命令;把这三处抽成变量是为了让自检不真的联网、也不真的 ssh。
AGENT_RELEASE_REPO="${DAEDALUS_AGENT_RELEASE_REPO:-Daedalusys/daedalus-agent}"
AGENT_GH="${DAEDALUS_GH:-gh}"
AGENT_SSH="${DAEDALUS_SSH:-ssh}"
AGENT_BIN="${DAEDALUS_AGENT_BIN:-/opt/daedalus-agent/bin/daedalus-agent}"

# 自检用的临时目录。走全局变量而非 run_selftest 的 local:trap 在函数返回之后才
# 触发,那时 local 已经出作用域,单引号 trap 里的 $SELFTEST_TMP 只能指向全局。
SELFTEST_TMP=""

# ------------------------------------------------------------- 命名与取件 ----

# agent_asset_name <version> <arch>:release 资产名。
#
# 命名只存在这一处:取件(download 的 --pattern)、断言脚本、评测口径都从这里取,
# 名字漂开就会 404,而人看到的只有"取件失败"。
agent_asset_name() {
    local version="${1:-}" arch="${2:-}"
    if [ -z "$version" ] || [ -z "$arch" ]; then
        echo "用法: agent_asset_name <version> <arch>" >&2
        return 1
    fi
    version="${version#v}"
    if [ -z "$version" ]; then
        echo "agent_asset_name: 版本号为空(只给了 v 前缀)" >&2
        return 1
    fi
    # uname -m 说 x86_64/aarch64,release 资产说 amd64/arm64 —— 两套词表各自都对,
    # 直接拼接就拼出一个不存在的名字,故在入口处归一,全套逻辑只认 release 那套。
    case "$arch" in
        amd64 | x86_64) arch=amd64 ;;
        arm64 | aarch64) arch=arm64 ;;
        *)
            echo "agent_asset_name: 不支持的架构 ${arch}(只支持 amd64 / arm64)" >&2
            return 1
            ;;
    esac
    printf 'daedalus-agent_%s_linux_%s.tar.gz\n' "$version" "$arch"
}

# fetch_agent_release <version> <destdir>:取件 + 校验,成功时 stdout 只留绝对路径。
#
# 任何一步失败都 return 1,绝不 `|| true`:校验和比对被吞掉等于把截断/篡改的包
# 装到机器上,而调用方还会以为"取到了"。
fetch_agent_release() {
    local rawver="${1:-}" destdir="${2:-}"
    if [ -z "$rawver" ] || [ -z "$destdir" ]; then
        echo "用法: fetch_agent_release <version> <destdir>" >&2
        return 1
    fi
    local version tag asset
    version="${rawver#v}"
    tag="v${version}"
    # 取件跑在驱动机上,故按驱动机的 arch 取:被控机与驱动机同架构是靶场的前提。
    # DAEDALUS_AGENT_ARCH 可在跨架构取件时改写。
    asset="$(agent_asset_name "$version" "${DAEDALUS_AGENT_ARCH:-$(uname -m)}")" || return 1

    mkdir -p "$destdir" || return 1
    # 同一个 destdir 二次取件时,上一轮的残包会让 sha256sum 去校验旧文件(比对
    # 通过,但内容不是这一次下载的)。先清掉这两个名字,让"校验通过"只覆盖本轮。
    rm -f "$destdir/$asset" "$destdir/SHA256SUMS" || return 1

    # 为什么连另一个 arch 的包一起下:SHA256SUMS 是一次发布的**全量**清单(amd64
    # + arm64),只下一个包时 `sha256sum -c --strict` 会因"清单里另一行读不到文件"
    # 整体判失败(coreutils:列为失败即 exit 1),把一次正常取件误报成校验失败。
    # 下全量再整体 -c 顺带把另一个 arch 也核了,代价只是几十 MB 流量。
    if ! "$AGENT_GH" release download "$tag" --repo "$AGENT_RELEASE_REPO" \
        --pattern '*.tar.gz' --pattern SHA256SUMS -D "$destdir"; then
        echo "fetch_agent_release: 从 release ${tag} 取件失败(该 tag 无 release,或资产缺失)" >&2
        return 1
    fi

    local tarball="$destdir/$asset"
    if [ ! -f "$tarball" ]; then
        echo "fetch_agent_release: release ${tag} 里没有 ${asset}(资产命名与本层推导不一致?)" >&2
        return 1
    fi
    if [ ! -f "$destdir/SHA256SUMS" ]; then
        echo "fetch_agent_release: release ${tag} 缺 SHA256SUMS,无法校验来源,拒绝安装" >&2
        return 1
    fi

    # LC_ALL=C:让成功/失败行固定成英文,证据文件才能跨宿主逐字比对(与
    # agent-e2e.sh 的 pkg_probe_cmd 同一条理由)。
    local out rc=0
    out="$(cd "$destdir" && LC_ALL=C sha256sum -c --strict SHA256SUMS 2>&1)" || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "fetch_agent_release: checksum 校验失败(${destdir}/SHA256SUMS,$(printf '%s' "$asset"))" >&2
        printf '%s\n' "$out" >&2
        return 1
    fi

    printf '%s/%s\n' "$(cd "$destdir" && pwd)" "$asset"
}

# ------------------------------------------------------------- 版本断言 ----

# assert_agent_version <ssh别名> <expect-version>:在**目标机**上跑已安装二进制
# 的 version 子命令,要求输出含 "<版本> commit="。
#
# 为什么非要 commit=:裸构建(不带 ldflags)只会自报 `0.1.0-dev`,拼不出 commit;
# 能拼出 commit= 才同时证明两件事 —— 机器上装的是这个 tag,且是发布产物。
assert_agent_version() {
    local alias="${1:-}" expect="${2:-}"
    if [ -z "$alias" ] || [ -z "$expect" ]; then
        echo "用法: assert_agent_version <ssh别名> <expect-version>" >&2
        return 1
    fi
    local out rc=0
    out="$("$AGENT_SSH" -n "$alias" "$AGENT_BIN version" 2>&1)" || rc=$?
    echo "  [版本核实] ${alias}: \$ ${AGENT_BIN} version"
    printf '%s\n' "$out" | sed 's/^/    /'
    if [ "$rc" -ne 0 ]; then
        echo "assert_agent_version: ${alias} 上跑 version 失败(rc=${rc},二进制没装上或不可执行)" >&2
        return 1
    fi
    if [[ "$out" != *"${expect} commit="* ]]; then
        echo "assert_agent_version: ${alias} 自报版本不含 \"${expect} commit=\"" >&2
        return 1
    fi
    return 0
}

# ---------------------------------------------------------------- selftest ----

run_selftest() {
    local tmp pass=0 fail=0
    tmp="$(mktemp -d)"
    SELFTEST_TMP="$tmp"
    trap 'rm -rf "$SELFTEST_TMP"' EXIT

    ok() { printf '  PASS  %s\n' "$*"; pass=$((pass + 1)); }
    bad() { printf '  FAIL  %s\n' "$*"; fail=$((fail + 1)); }

    # --- 夹具:两个 arch 的真 tar.gz(与 release 版式一致)+ 同形 SHA256SUMS ---
    local fx="$tmp/fixtures" ver="0.1.0" a mk
    mkdir -p "$fx"
    for a in amd64 arm64; do
        mk="$tmp/mk-$a/daedalus-agent_${ver}_linux_$a"
        mkdir -p "$mk/bin" "$mk/scripts"
        printf '#!/bin/sh\necho 假二进制\n' >"$mk/bin/daedalus-agent"
        printf '#!/bin/sh\nexit 0\n' >"$mk/scripts/install.sh"
        (cd "$tmp/mk-$a" && tar -czf "$fx/daedalus-agent_${ver}_linux_$a.tar.gz" \
            "daedalus-agent_${ver}_linux_$a")
    done
    (cd "$fx" && LC_ALL=C sha256sum ./*.tar.gz | sed 's|\./||' >SHA256SUMS)

    # --- 假 gh / 假 ssh,PATH 前置;不联网、不真 ssh ---
    mkdir -p "$tmp/bin"
    cat >"$tmp/bin/gh" <<'EOG'
#!/usr/bin/env bash
# 假 gh:只实现本层用到的那一种调用 ——
#   gh release download <tag> --repo <R> --pattern <P>... -D <DIR>
# 只把 pattern 命中的夹具 cp 进 DIR,与真 gh 的取件语义一致,好让"资产缺失"
# 这件事在自检里能被真的触发(而不是靠假 gh 直接返回 1 假装)。
set -uo pipefail
if [ "${FAKE_GH_FAIL:-0}" = 1 ]; then
    echo "release not found (假 gh 注入的失败)" >&2
    exit 1
fi
if [ "${1:-}" != release ] || [ "${2:-}" != download ]; then
    echo "假 gh: 只实现 release download" >&2
    exit 2
fi
shift 2
dest="" tag="" hit=0
patterns=()
while [ "$#" -gt 0 ]; do
    case "$1" in
        --repo) shift 2 ;;
        --pattern) patterns+=("$2"); shift 2 ;;
        -D) dest="$2"; shift 2 ;;
        *) [ -n "$tag" ] || tag="$1"; shift ;;
    esac
done
: "${tag:?假 gh: 缺 tag}"
: "${dest:?假 gh: 缺 -D}"
mkdir -p "$dest"
for p in ${patterns[@]+"${patterns[@]}"}; do
    hit=0
    for f in "$FAKE_GH_FIXTURES"/*; do
        b="$(basename "$f")"
        # shellcheck disable=SC2254  # 这里要的就是 glob 匹配,不是字面量
        case "$b" in $p) cp "$f" "$dest/$b" && hit=$((hit + 1)) ;; esac
    done
    [ "$hit" -gt 0 ] || { echo "no assets match pattern $p (假 gh)" >&2; exit 1; }
done
EOG
    cat >"$tmp/bin/ssh" <<'EOS'
#!/usr/bin/env bash
# 假 ssh:真机那侧本脚本只读被控端 stdout,故这里原样吐出预置的 version 输出。
# 参数(-n / 别名 / 远端命令)全部忽略 —— 被测的是"怎么判输出",不是 ssh 本身。
printf '%s\n' "${FAKE_SSH_OUT:-daedalus-agent 0.1.0 commit=abc1234 built=2026-11-15T04:00:00Z}"
EOS
    chmod +x "$tmp/bin/gh" "$tmp/bin/ssh"
    PATH="$tmp/bin:$PATH"
    export PATH FAKE_GH_FIXTURES="$fx"

    local out rc

    # --- 用例 1/5:资产名 ---
    out="$(agent_asset_name 0.1.0 arm64)" && rc=0 || rc=$?
    if [ "$rc" -eq 0 ] && [ "$out" = "daedalus-agent_0.1.0_linux_arm64.tar.gz" ]; then
        ok "用例 1/5 资产名 = $out"
    else
        bad "用例 1/5 资产名(rc=$rc,实际 ${out:-<空>})"
    fi

    # --- 用例 2/5:取件成功,返回的是存在的本地 tarball ---
    out="$(fetch_agent_release 0.1.0 "$tmp/dl")" && rc=0 || rc=$?
    local want_name
    want_name="$(agent_asset_name 0.1.0 "$(uname -m)")"
    if [ "$rc" -eq 0 ] && [ -f "$out" ] && [ "$(basename "$out")" = "$want_name" ]; then
        ok "用例 2/5 取件成功且校验和通过:$out"
    else
        bad "用例 2/5 取件(rc=$rc,返回 ${out:-<空>},期望 basename $want_name)"
    fi

    # --- 用例 3/5:资产缺失(假 gh 返回 1)→ 非零,文案点名 release ---
    out="$(FAKE_GH_FAIL=1 fetch_agent_release 0.1.0 "$tmp/dl-missing" 2>&1)" && rc=0 || rc=$?
    if [ "$rc" -ne 0 ] && [[ "$out" == *release* ]]; then
        ok "用例 3/5 资产缺失 → rc=$rc,文案含 release"
    else
        bad "用例 3/5 资产缺失未按预期失败(rc=$rc,输出 ${out:-<空>})"
    fi

    # --- 用例 4/5:SHA256SUMS 里改掉一位中的整段哈希 → 非零,文案点名 checksum ---
    local fxbad="$tmp/fixtures-bad"
    cp -r "$fx" "$fxbad"
    # 把第一行(amd64)的哈希换成 64 个 0:任何真包的哈希都不会恰好是全零,故这是
    # 一次确定的"篡改一位以上"。用 awk 重写而不是 sed 替换首字符 —— 首字符本身
    # 就可能已经是 0,那样等于没改,用例会假绿。
    LC_ALL=C awk 'NR==1 { printf "%064d  %s\n", 0, $2; next } { print }' \
        "$fxbad/SHA256SUMS" >"$fxbad/SHA256SUMS.tmp" && mv "$fxbad/SHA256SUMS.tmp" "$fxbad/SHA256SUMS"
    out="$(FAKE_GH_FIXTURES="$fxbad" fetch_agent_release 0.1.0 "$tmp/dl-bad" 2>&1)" && rc=0 || rc=$?
    if [ "$rc" -ne 0 ] && [[ "$out" == *checksum* ]]; then
        ok "用例 4/5 校验和不符 → rc=$rc,文案含 checksum"
    else
        bad "用例 4/5 校验和不符未被拦住(rc=$rc,输出 ${out:-<空>})"
    fi

    # --- 用例 5/5:版本断言(正:含 `<版本> commit=`;负:期望别的版本必须拒) ---
    local pos=0 neg=0
    FAKE_SSH_OUT="daedalus-agent 0.1.0 commit=abc1234 built=2026-11-15T04:00:00Z" \
        assert_agent_version 假机 0.1.0 >/dev/null 2>&1 && pos=1
    FAKE_SSH_OUT="daedalus-agent 0.1.0 commit=abc1234 built=2026-11-15T04:00:00Z" \
        assert_agent_version 假机 0.2.0 >/dev/null 2>&1 && neg=1
    if [ "$pos" -eq 1 ] && [ "$neg" -eq 0 ]; then
        ok "用例 5/5 assert_agent_version:自报 0.1.0 commit=… 过,期望 0.2.0 拒"
    else
        bad "用例 5/5 assert_agent_version(pos=$pos neg=$neg;正例须过、反例须拒)"
    fi

    echo
    if [ "$fail" -eq 0 ]; then
        printf 'RESULT: SELFTEST PASS (%d/5)\n' "$pass"
        return 0
    fi
    printf 'RESULT: SELFTEST FAIL (%d/5,失败 %d)\n' "$pass" "$fail"
    return 1
}

# 只有**直接执行**时才跑自检;被 agent-e2e.sh source 时本文件只贡献函数,
# 不动调用方的 shell 选项(它自己已有 set -euo pipefail)。
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    set -euo pipefail
    case "${1:-}" in
        --selftest) run_selftest ;;
        *)
            sed -n '2,30p' "${BASH_SOURCE[0]}"
            exit 2
            ;;
    esac
fi
