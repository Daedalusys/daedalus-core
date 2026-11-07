#!/usr/bin/env bash
# artifact-fingerprint.sh 的判定逻辑自检:不碰 qemu、不需要 KVM、不需要真镜像。
set -euo pipefail
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
. "$HERE/artifact-fingerprint.sh"

fail=0
st() { if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
       else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail=$((fail + 1)); fi }

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
fp="$tmp/fp"

# 1) 无指纹文件 → 不匹配(必须触发重建,不能当一致)
if artifact_fp_match "$fp" "sha256:aaa" 2>/dev/null; then rc=0; else rc=$?; fi
st 1 "$rc" "指纹文件不存在 → 不匹配"

# 2) 写入后可读回、且匹配
artifact_fp_write "$fp" "sha256:aaa"
st "sha256:aaa" "$(cat "$fp")" "写入后可读回"
if artifact_fp_match "$fp" "sha256:aaa"; then rc=0; else rc=$?; fi
st 0 "$rc" "指纹一致 → 匹配"

# 3) 镜像变了 → 不匹配
if artifact_fp_match "$fp" "sha256:bbb"; then rc=0; else rc=$?; fi
st 1 "$rc" "指纹不同 → 不匹配"

# 4) 指纹文件为空 → 不匹配(损坏不能 fail-open)
: > "$fp"
if artifact_fp_match "$fp" "sha256:aaa"; then rc=0; else rc=$?; fi
st 1 "$rc" "指纹文件为空 → 不匹配"

# 5) 写入是原子的:tmp 文件不残留
artifact_fp_write "$fp" "sha256:ccc"
st 0 "$(ls "$tmp" | grep -c '\.tmp$' || true)" "写入后无 .tmp 残留"

# 6) describe 对不可读文件给出可读占位
st "<无>" "$(artifact_fp_describe "$tmp/nonexistent")" "describe 对缺失文件 → <无>"

# 7) artifact_fingerprint 自身:它是所有判定的源头,拿到空值/<none> 必须显式失败。
#    这里用 stub 覆盖 podman,因为真实 podman 在不同版本上对本地构建镜像的 digest
#    行为不一,断言不该依赖那种差异。
STUB_EXISTS_RC=0
STUB_INSPECT=""
STUB_PULL_RC=0
STUB_ID=""
podman() {
    case "${1:-}" in
        image)
            case "${2:-}" in
                exists) return "$STUB_EXISTS_RC" ;;
                inspect)
                    # podman inspect 的格式模板在参数里(不是最后一个参数):
                    # `podman image inspect --format '{{.Digest}}' <ref>`。
                    case "$*" in
                        *{{.Id}}*)    printf '%s\n' "$STUB_ID" ;;
                        *{{.Digest}}*) printf '%s\n' "$STUB_INSPECT" ;;
                        *)            printf '%s\n' "$STUB_INSPECT" ;;
                    esac
                    ;;
            esac
            ;;
        pull) return "$STUB_PULL_RC" ;;
    esac
    return 0
}

# 指纹键必须是内容标识(.Id):tag 会被重推,manifest digest 也随之变。
STUB_EXISTS_RC=0 STUB_ID="sha256:configid" STUB_INSPECT="sha256:manifestdigest"
st "sha256:configid" "$(artifact_fingerprint some-ref)" "指纹取 config digest(.Id),不是 manifest digest"

# 同一份镜像重推后 manifest digest 变了,指纹必须不变(这是真机实测到的漂移)。
STUB_INSPECT="sha256:anothermanifest"
st "sha256:configid" "$(artifact_fingerprint some-ref)" "manifest digest 漂移不影响指纹"

# 本地构建的镜像只有 .Id,没有 RepoDigests → 仍能取到指纹(不假红)。
STUB_ID="sha256:localbuild" STUB_INSPECT="<none>"
st "sha256:localbuild" "$(artifact_fingerprint local-ref)" "本地构建镜像(.Digest=<none>)→ 用 .Id"

# 两个都取不到 → 显式失败(空指纹会让所有比对都"匹配")。
STUB_ID="" STUB_INSPECT=""
if artifact_fingerprint some-ref >/dev/null 2>&1; then rc=0; else rc=$?; fi
st 1 "$rc" ".Id 与 .Digest 都取不到 → 显式失败"

STUB_ID="<none>" STUB_INSPECT="<none>"
if artifact_fingerprint some-ref >/dev/null 2>&1; then rc=0; else rc=$?; fi
st 1 "$rc" ".Id 与 .Digest 都是 <none> → 显式失败"

# 拉不到镜像 → 显式失败
STUB_EXISTS_RC=1 STUB_ID="" STUB_INSPECT="" STUB_PULL_RC=1
if artifact_fingerprint some-ref >/dev/null 2>&1; then rc=0; else rc=$?; fi
st 1 "$rc" "本地无镜像且拉取失败 → 显式失败"

if [ "$fail" -eq 0 ]; then echo "RESULT: ARTIFACT-FP SELFTEST PASS"; exit 0; fi
echo "RESULT: ARTIFACT-FP SELFTEST FAIL ($fail)"; exit 1
