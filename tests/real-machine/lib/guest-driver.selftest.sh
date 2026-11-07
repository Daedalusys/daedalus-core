#!/usr/bin/env bash
# =============================================================================
# guest-driver.selftest.sh —— 驱动库自检(不真起 QEMU,用 stub 覆盖外部命令)
# =============================================================================
# 为什么需要
#   驱动库的判定逻辑(结论**累积**、任一 FAIL 即非零)与幂等(已有产物不重跑
#   ~7GB 派生构建)都是「改断言就不可验证」的那类代码:没有自检,把累积改成
#   「最后一次覆盖」或把幂等改瞎,不会有任何东西发现。
# =============================================================================
set -uo pipefail

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

export VERIFY_WORK="$TMP/work"
mkdir -p "$VERIFY_WORK"

# shellcheck source=guest-driver.sh
source "$(dirname "${BASH_SOURCE[0]}")/guest-driver.sh"

fail_n=0
ck() { # ck <说明> <期望> <实际>
    if [ "$2" = "$3" ]; then printf '  PASS  %s\n' "$1"
    else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$1" "$2" "$3"; fail_n=$((fail_n + 1)); fi
}

printf '== guest-driver.selftest ==\n'

# 1) FAIL 之后 driver_exit 非零
( DRIVER_FAIL_N=0; driver_verdict FAIL "x" >/dev/null; driver_exit >/dev/null 2>&1 )
ck "FAIL → driver_exit 非零" 1 $?

# 2) 累积语义:先 PASS 再 FAIL 最终仍非零(不是最后一次覆盖)
( DRIVER_FAIL_N=0; driver_verdict PASS "a" >/dev/null; driver_verdict FAIL "b" >/dev/null; driver_exit >/dev/null 2>&1 )
ck "PASS 后 FAIL 仍非零(累积)" 1 $?

# 3) 全 PASS → 零
( DRIVER_FAIL_N=0; driver_verdict PASS "a" >/dev/null; driver_verdict PASS "b" >/dev/null; driver_exit >/dev/null 2>&1 )
ck "全 PASS → driver_exit 零" 0 $?

# 4) 幂等:工作目录已有 qcow2、派生镜像、**且指纹与被测镜像一致**时 driver_init
#    不得重跑 podman build。
#    stub 必须能回答 digest:指纹判据拿不到 digest 时会 fail-closed 拒跑(那是刻意的),
#    若 stub 沉默,这里测到的就不是复用逻辑而是拒跑路径。
PODMAN_DIGEST="sha256:cur"
build_n=0
run_n=0
podman() {
    case "${1:-}" in
        build) build_n=$((build_n + 1)) ;;
        run)
            run_n=$((run_n + 1))
            # 模拟 bootc-image-builder 的产出:没有它就走到「builder 没产出 qcow2」。
            # EXTRA_QCOW2=1 时额外留一个「删不掉的」旧产物,用来钉住「按名字取第一个
            # 可能取到旧的」这条失效路径。
            mkdir -p "$VERIFY_WORK/output/qcow2"
            printf 'qcow2' > "$VERIFY_WORK/output/qcow2/disk.qcow2"
            [ "${EXTRA_QCOW2:-0}" = 1 ] && printf 'old' > "$VERIFY_WORK/output/qcow2/stale.qcow2"
            ;;
        image)
            case "${2:-}" in
                inspect) printf '%s\n' "$PODMAN_DIGEST" ;;
            esac
            ;;
    esac
    return 0
}
mkdir -p "$VERIFY_WORK/output/qcow2"
# 产物必须非空:复用判据用 -s,空文件等于「没有产物」(零字节残留不许被当 qcow2)。
printf 'qcow2' > "$VERIFY_WORK/output/qcow2/disk.qcow2"
head -c6 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$VERIFY_WORK/.verify-pass"
artifact_fp_write "$VERIFY_WORK/.qcow2-fingerprint" "$PODMAN_DIGEST"
driver_init >/dev/null 2>&1
ck "幂等 driver_init rc=0" 0 $?
ck "幂等:已有产物时不重跑 podman build" 0 "$build_n"
ck "幂等:qcow2 路径被识别" 1 "$([ -n "${DRIVER_QCOW2:-}" ] && echo 1 || echo 0)"

# 4b) 零字节 qcow2 + 指纹一致 → 仍必须重建(指纹对得上不代表产物可用)。
: > "$VERIFY_WORK/output/qcow2/disk.qcow2"
build_n=0
driver_init >/dev/null 2>&1
ck "空 qcow2(指纹一致)→ 重建" 1 "$build_n"
ck "空 qcow2 重建后产物非空" 1 "$([ -s "$VERIFY_WORK/output/qcow2/disk.qcow2" ] && echo 1 || echo 0)"

# 5) 本次事故的回归钉子:镜像换了 digest(qcow2 还在)→ 必须重建,不许复用。
#    旧判据只看「文件在 + 派生镜像在」,这样就会拿上一次 run 的产物冒充本次结果。
PODMAN_DIGEST="sha256:new"
build_n=0
driver_init >/dev/null 2>&1
ck "指纹不符 → 重建 rc=0" 0 $?
ck "指纹不符:确实重建(podman build 跑了一次)" 1 "$build_n"
ck "指纹不符:指纹被更新为新 digest" "sha256:new" "$(cat "$VERIFY_WORK/.qcow2-fingerprint")"

# 6) 指纹文件缺失(旧版本的缓存目录)→ 必须重建,不能当一致。
rm -f "$VERIFY_WORK/.qcow2-fingerprint"
build_n=0
driver_init >/dev/null 2>&1
ck "无指纹文件 → 重建" 1 "$build_n"

# 7) VERIFY_FORCE_REBUILD=1 → 即使指纹一致也重建(显式逃生门)。
build_n=0
VERIFY_FORCE_REBUILD=1 driver_init >/dev/null 2>&1
ck "VERIFY_FORCE_REBUILD=1 → 重建" 1 "$build_n"

# 8) 产物目录里多于一个 .qcow2 → 必须拒跑:按名字取第一个可能取到旧的,
#    而指纹会写到它头上(拿旧产物冒充本次结果)。
#    先换 digest 逼它走重建路径,否则会走复用分支直接返回。
PODMAN_DIGEST="sha256:multi"
rc=0
( EXTRA_QCOW2=1 driver_init ) >/dev/null 2>&1 || rc=$?
ck "产物目录有多个 qcow2 → 拒跑" 1 "$rc"
ck "多个 qcow2 时没有写指纹(不能给不确定的产物盖章)" \
   1 "$([ -f "$VERIFY_WORK/.qcow2-fingerprint" ] && echo 1 || echo 0)"
rm -f "$VERIFY_WORK/output/qcow2/stale.qcow2"

printf '\n'
if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
