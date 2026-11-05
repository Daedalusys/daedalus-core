#!/usr/bin/env bash
# 镜像最终态断言:构建期 lint(Containerfile 末尾的 `RUN bootc container lint`)之外的
# 一次复验 —— 从「以该镜像起一个容器」的视角,确认镜像仍满足构建期声称的不变式。
#
# 为什么独立成脚本:此前 CI 该步是两条 `podman run … || true` + 一句 echo,恒绿且什么
# 都没判定(core#60 同批假绿之一)。断言必须在脚本内 fail-closed;而脚本本身也要能被
# 验证,故 `--selftest` 用**假 podman** 干跑一遍,证明「哪条命令被调用、失败时退出码
# 非零」这两件事没变瞎,不依赖 podman / 镜像。
#
# 用法:
#   image-lint.sh [IMAGE_REF]             # 需 podman + 已构建镜像
#   image-lint.sh [IMAGE_REF] --selftest  # 只验判定逻辑,不碰真 podman
set -euo pipefail

IMAGE="${1:-localhost/daedalus-os:latest}"

# ---------------------------------------------------------------- 断言实现 -----
# run_one <说明> <容器内 argv…>:在镜像里跑一条命令,非零即判失败并立即退出(fail-closed)。
# 失败时**透传被断言命令的退出码**,让 CI 日志能直接看出是哪条、什么原因(如 podman 125)。
run_one() {
    local label="$1" rc=0; shift
    printf '  ...  %s\n' "$label"
    podman run --rm "${IMAGE}" "$@" || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "FAIL: ${label}(退出码 ${rc})" >&2
        return "$rc"
    fi
    printf '  OK   %s\n' "$label"
}

# run_checks —— 断言清单。顺序即执行顺序;任何一条失败,整步红。
run_checks() {
    # 构建期已在 Containerfile 末尾跑过一次 lint;这里再跑一次是为了让「镜像最终态」
# 有一个会真红的门 —— 该步历史上是 `podman run … || true`,从未判定过任何东西。
    run_one "bootc container lint" bootc container lint
    # /etc/os-release 的 VARIANT 标记是 daedalus-host rescue 与 headless-variant.sh
    # 共同依赖的契约(Containerfile 追加 VARIANT=headless);缺了镜像就不自证变体。
    run_one "VARIANT=headless 标记" grep -q '^VARIANT=headless$' /etc/os-release
    # 宿主 CLI 是镜像内一切插件操作/救援路径的入口,必须是可执行文件。
    run_one "宿主 CLI 可执行(/usr/local/bin/daedalus-host)" test -x /usr/local/bin/daedalus-host
    # QA 链路端到端:daedalus-smoke 在镜像内自检并通过。它此前从未装进镜像,
    # 于是 tests/deploy/headless-test.sh 的 F1 恒 SKIP(假绿);装上之后退出码即判据。
    run_one "qa: daedalus-smoke 端到端" /usr/local/bin/daedalus-smoke
}

# ---------------------------------------------------------------- 自检 ---------
# 用假 podman 干跑,断言「命令清单」与「失败传播」两件事。不碰真 podman / 镜像。
if [ "${1:-}" = "--selftest" ] || [ "${2:-}" = "--selftest" ]; then
    SELF="${BASH_SOURCE[0]}"
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    fail_n=0
    st() { # st <期望> <实际> <说明>
        if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
        else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail_n=$((fail_n + 1)); fi
    }

    cat >"$tmp/podman" <<'EOS'
#!/usr/bin/env bash
# 假 podman:记录调用;PODMAN_FAIL_AT 指定第几次调用返回 99,用于验证失败传播。
printf '%s\n' "$*" >>"$PODMAN_LOG"
n="$(wc -l <"$PODMAN_LOG")"
if [ "${PODMAN_FAIL_AT:-0}" = "$n" ]; then exit 99; fi
exit 0
EOS
    chmod +x "$tmp/podman"

    # 期望的命令清单(与 run_checks 逐字对应)。
    expected_log() {
        cat <<'EOF'
run --rm test-image bootc container lint
run --rm test-image grep -q ^VARIANT=headless$ /etc/os-release
run --rm test-image test -x /usr/local/bin/daedalus-host
run --rm test-image /usr/local/bin/daedalus-smoke
EOF
    }

    printf '== image-lint.sh --selftest ==\n'
    # 全绿:退出码 0,且 podman 调用清单逐字等于期望。
    rm -f "$tmp/log"
    if PATH="$tmp:$PATH" PODMAN_LOG="$tmp/log" bash "$SELF" test-image >/dev/null 2>&1; then rc=0; else rc=$?; fi
    st 0 "$rc" "全部断言通过 → 退出码 0"
    st "$(expected_log)" "$(cat "$tmp/log" 2>/dev/null || true)" "podman 调用清单与顺序逐字一致"

    # 失败传播:第 1 条(bootc lint)失败 → 非零。
    rm -f "$tmp/log"
    if PATH="$tmp:$PATH" PODMAN_LOG="$tmp/log" PODMAN_FAIL_AT=1 bash "$SELF" test-image >/dev/null 2>&1; then rc=0; else rc=$?; fi
    st 99 "$rc" "第 1 条断言失败 → 退出码透传(非零)"

    # 失败传播:最后一条(daedalus-smoke)失败 → 非零。
    # 末尾序号从期望清单派生,不写死 —— 否则将来在 run_checks 里追加一条断言,
    # 本用例会静默变成"测中间那条"却仍自称"最后一条"。
    last_at="$(expected_log | grep -c .)"
    rm -f "$tmp/log"
    if PATH="$tmp:$PATH" PODMAN_LOG="$tmp/log" PODMAN_FAIL_AT="$last_at" bash "$SELF" test-image >/dev/null 2>&1; then rc=0; else rc=$?; fi
    st 99 "$rc" "最后一条断言失败 → 退出码透传(非零)"

    if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
    echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
fi

# ---------------------------------------------------------------- 正式流程 -----
command -v podman >/dev/null \
    || { echo "image-lint: 缺 podman,无法在镜像内断言(fail-closed)" >&2; exit 1; }
echo "== 镜像最终态断言:${IMAGE} =="
run_checks
echo "OK: 镜像 lint + VARIANT + 宿主 CLI + daedalus-smoke 断言通过"
