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

# 7)+) artifact_fingerprint 与新鲜度判据:用 stub 覆盖 podman/skopeo。
#    真实 podman 在不同版本上对本地构建镜像的 digest 行为不一,断言不该依赖那种差异。
#    pull 次数记在文件里而不是变量里:被检函数在 $( ) 子 shell 中运行,变量改动不会传回。
PULL_COUNT="$tmp/pull.calls"
stub_reset() {
    STUB_EXISTS_RC=0
    STUB_INSPECT=""
    STUB_PULL_RC=0
    STUB_ID=""
    STUB_REMOTE_DIGEST=""
    STUB_REMOTE_RC=0
    STUB_LOCAL_DIGEST=""
    : > "$PULL_COUNT"
}
pull_calls() { wc -l < "$PULL_COUNT" | tr -d ' '; }

podman() {
    case "${1:-}" in
        image)
            case "${2:-}" in
                exists) return "$STUB_EXISTS_RC" ;;
                inspect)
                    # podman inspect 的格式模板在参数里(不是最后一个参数):
                    # `podman image inspect --format '{{.Digest}}' <ref>`。
                    case "$*" in
                        *{{.Id}}*)     printf '%s\n' "$STUB_ID" ;;
                        *{{.Digest}}*) printf '%s\n' "${STUB_LOCAL_DIGEST:-$STUB_INSPECT}" ;;
                        *)             printf '%s\n' "$STUB_INSPECT" ;;
                    esac
                    ;;
            esac
            ;;
        pull)
            # 计数落文件:调用方是命令替换里的子 shell,变量改不回去。
            printf 'pull\n' >> "$PULL_COUNT"
            return "$STUB_PULL_RC"
            ;;
    esac
    return 0
}
skopeo() {
    # STUB_REMOTE_RC 用来模拟 registry 不可达 / 无凭据可查远端。
    [ "$STUB_REMOTE_RC" = "0" ] || return "$STUB_REMOTE_RC"
    [ "${1:-}" = inspect ] || return 1
    printf '%s\n' "$STUB_REMOTE_DIGEST"
}

stub_reset
# 指纹键必须是内容标识(.Id):tag 会被重推,manifest digest 也随之变。
STUB_ID="sha256:configid" STUB_INSPECT="sha256:manifestdigest"
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

# 8) 可变 tag 判定:哪些 ref 需要刷新,哪些不用
st 0 "$(artifact_fp_ref_is_mutable ghcr.io/x/y:latest && echo 0 || echo 1)" "latest → 可变"
st 0 "$(artifact_fp_ref_is_mutable ghcr.io/x/y:main && echo 0 || echo 1)" "main → 可变"
st 1 "$(artifact_fp_ref_is_mutable ghcr.io/x/y:851bbcf8fa382aab2620126f2d2b5a95445e11c5 && echo 0 || echo 1)" "commit sha tag → 不可变"
st 1 "$(artifact_fp_ref_is_mutable ghcr.io/x/y:v1.2.3 && echo 0 || echo 1)" "版本 tag → 不可变"
st 1 "$(artifact_fp_ref_is_mutable ghcr.io/x/y@sha256:abc && echo 0 || echo 1)" "钉 digest → 不可变"
st 0 "$(artifact_fp_ref_is_mutable ghcr.io:5000/x/y:latest && echo 0 || echo 1)" "带端口的 latest → 可变"
st 1 "$(artifact_fp_ref_is_mutable ghcr.io:5000/x/y:851bbcf8fa382aab2620126f2d2b5a95445e11c5 && echo 0 || echo 1)" "带端口的 sha tag → 不可变"

# 9) 远端 digest 与本地一致 → 不触发 pull
stub_reset
STUB_REMOTE_DIGEST=sha256:same STUB_LOCAL_DIGEST=sha256:same
st 0 "$(artifact_fp_refresh ghcr.io/x/y:851bbcf8fa382aab2620126f2d2b5a95445e11c5 && echo 0 || echo 1)" \
   "远端==本地 → 不 pull"
st 0 "$(pull_calls)" "远端==本地 → 确实没有调用 podman pull"

# 10) 远端 digest 与本地不同(同 tag 被重推) → 必须 pull
stub_reset
STUB_REMOTE_DIGEST=sha256:new STUB_LOCAL_DIGEST=sha256:old STUB_PULL_RC=1
st 1 "$(artifact_fp_refresh ghcr.io/x/y:851bbcf8fa382aab2620126f2d2b5a95445e11c5 >/dev/null 2>&1 && echo 0 || echo 1)" \
   "远端≠本地 → 触发 pull(此处桩 make pull 失败以证明它被调用)"
st 1 "$(pull_calls)" "远端≠本地 → 确实调用了 podman pull"

# 11) 40 位 sha tag 也走同一判据(不再被当不可变跳过)
stub_reset
STUB_REMOTE_DIGEST=sha256:new STUB_LOCAL_DIGEST=sha256:old STUB_PULL_RC=1
st 1 "$(artifact_fp_refresh ghcr.io/x/y:0123456789abcdef0123456789abcdef01234567 >/dev/null 2>&1 && echo 0 || echo 1)" \
   "sha tag 同样按远端判据"

# 12) 本地自建 tag 不尝试 pull
stub_reset
STUB_PULL_RC=1
st 0 "$(artifact_fp_refresh localhost/daedalus-verify:base && echo 0 || echo 1)" \
   "本地自建 tag → 不 pull,直接成功"
st 0 "$(pull_calls)" "本地自建 tag → 没有调用 podman pull"

# 13) 查不到远端 → 显式失败(除非 VERIFY_OFFLINE=1)
stub_reset
STUB_REMOTE_RC=1
st 1 "$(artifact_fp_refresh ghcr.io/x/y:abc >/dev/null 2>&1 && echo 0 || echo 1)" \
   "查不到远端 → 失败"
st 0 "$(VERIFY_OFFLINE=1 artifact_fp_refresh ghcr.io/x/y:abc >/dev/null 2>&1 && echo 0 || echo 1)" \
   "VERIFY_OFFLINE=1 → 跳过校验但放行"

# 13b) 没有 skopeo → 显式失败(不许静默当成「一致」)
st 1 "$( unset -f skopeo; artifact_fp_refresh ghcr.io/x/y:abc >/dev/null 2>&1 && echo 0 || echo 1)" \
   "缺 skopeo → 显式失败"

# 14) 本地根本没有该镜像 → 直接拉,不报错
stub_reset
STUB_REMOTE_DIGEST=sha256:new
st 0 "$(artifact_fp_refresh ghcr.io/x/y:tag 2>/dev/null && echo 0 || echo 1)" \
   "本地无镜像 → 拉取成功即放行"
st 1 "$(pull_calls)" "本地无镜像 → 确实调用了 podman pull"

# 15) 本地无镜像且拉取失败 → 显式失败(不许退化成「没有就是最新」)
stub_reset
STUB_REMOTE_DIGEST=sha256:new STUB_PULL_RC=1
st 1 "$(artifact_fp_refresh ghcr.io/x/y:tag >/dev/null 2>&1 && echo 0 || echo 1)" \
   "本地无镜像且拉取失败 → 失败"

if [ "$fail" -eq 0 ]; then echo "RESULT: ARTIFACT-FP SELFTEST PASS"; exit 0; fi
echo "RESULT: ARTIFACT-FP SELFTEST FAIL ($fail)"; exit 1
