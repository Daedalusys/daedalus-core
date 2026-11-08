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
    STUB_PULL_STALE=0
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
                        *{{.Id}}*)         printf '%s\n' "$STUB_ID" ;;
                        *{{.RepoDigests}}*) if [ -n "${STUB_REPO_DIGESTS:-}" ]; then
                                               printf '%s\n' "$STUB_REPO_DIGESTS"
                                           elif [ -n "${STUB_LOCAL_DIGEST:-${STUB_INSPECT}}" ]; then
                                               printf '[example.invalid/x@%s]\n' "${STUB_LOCAL_DIGEST:-$STUB_INSPECT}"
                                           else
                                               printf '[]\n'
                                           fi ;;
                        *{{.Digest}}*)     printf '%s\n' "${STUB_LOCAL_DIGEST:-$STUB_INSPECT}" ;;
                        *)                 printf '%s\n' "$STUB_INSPECT" ;;
                    esac
                    ;;
            esac
            ;;
        pull)
            # 计数落文件:调用方是命令替换里的子 shell,变量改不回去。
            printf 'pull\n' >> "$PULL_COUNT"
            [ "$STUB_PULL_RC" = "0" ] || return "$STUB_PULL_RC"
            # 模拟真实拉取的结果:拉完本地就是远端那份内容。
            # STUB_PULL_STALE=1 用来模拟「registry 里这个 tag 还不是期望的那份」。
            [ "${STUB_PULL_STALE:-0}" = "1" ] || STUB_LOCAL_DIGEST="$STUB_REMOTE_DIGEST"
            return 0
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

# 16) 构建腿显式传下本次产出的 digest(VERIFY_IMAGE_DIGEST):有它就不问 registry,
#     本地不一致直接拉 —— registry 还没生效也不会用旧镜像判定。
#     桩里 STUB_REMOTE_DIGEST 表示「拉取之后本地会变成的那份内容」。
stub_reset
STUB_REMOTE_RC=1                       # 远端查询若被调用会失败,用它证明这条路径没查 registry
STUB_LOCAL_DIGEST=sha256:stale
STUB_REMOTE_DIGEST=sha256:expected
st 0 "$(VERIFY_IMAGE_DIGEST=sha256:expected artifact_fp_refresh ghcr.io/x/y:tag 2>/dev/null && echo 0 || echo 1)" \
   "VERIFY_IMAGE_DIGEST 与本地不一致 → 拉新后放行(未查 registry)"
st 1 "$(pull_calls)" "VERIFY_IMAGE_DIGEST 与本地不一致 → 确实调用了 podman pull"

# 17) 显式期望值覆盖「本地根本没有该镜像」:照样拉
stub_reset
STUB_REMOTE_RC=1
STUB_REMOTE_DIGEST=sha256:expected
st 0 "$(VERIFY_IMAGE_DIGEST=sha256:expected artifact_fp_refresh ghcr.io/x/y:tag 2>/dev/null && echo 0 || echo 1)" \
   "VERIFY_IMAGE_DIGEST + 本地无镜像 → 拉新"
st 1 "$(pull_calls)" "VERIFY_IMAGE_DIGEST + 本地无镜像 → 确实调用了 podman pull"

# 18) 拉了之后仍对不上期望值 → 显式失败(registry 里还不是本次那份,
#     继续判定等于拿别的镜像的结论冒充本次结果)
stub_reset
STUB_LOCAL_DIGEST=sha256:stale
STUB_REMOTE_DIGEST=sha256:somethingelse
STUB_PULL_STALE=1
st 1 "$(VERIFY_IMAGE_DIGEST=sha256:expected artifact_fp_refresh ghcr.io/x/y:tag >/dev/null 2>&1 && echo 0 || echo 1)" \
   "拉取后仍与 VERIFY_IMAGE_DIGEST 不一致 → 失败"

# 19) 本地已经就是本次产出的那份 → 不拉(只做一次本地 inspect)
stub_reset
STUB_REMOTE_RC=1
STUB_LOCAL_DIGEST=sha256:expected
st 0 "$(VERIFY_IMAGE_DIGEST=sha256:expected artifact_fp_refresh ghcr.io/x/y:tag 2>/dev/null && echo 0 || echo 1)" \
   "VERIFY_IMAGE_DIGEST 与本地一致 → 放行"
st 0 "$(pull_calls)" "VERIFY_IMAGE_DIGEST 与本地一致 → 没有调用 podman pull"

# 20) 同一个 tag 挂着多条 RepoDigests(重推后旧的不会消失),期望值只在其中一条上
#     → 内容其实就是期望那份,不许因为 .Digest 取到另一条就又拉一遍 2.2GB。
stub_reset
STUB_LOCAL_DIGEST=sha256:other
STUB_REPO_DIGESTS="[ghcr.io/x/y@sha256:other ghcr.io/x/y@sha256:expected]"
st 0 "$(VERIFY_IMAGE_DIGEST=sha256:expected artifact_fp_refresh ghcr.io/x/y:tag 2>/dev/null && echo 0 || echo 1)" \
   "期望值命中 RepoDigests 中的一条 → 放行"
st 0 "$(pull_calls)" "期望值命中 RepoDigests 中的一条 → 没有调用 podman pull"

if [ "$fail" -eq 0 ]; then echo "RESULT: ARTIFACT-FP SELFTEST PASS"; exit 0; fi
echo "RESULT: ARTIFACT-FP SELFTEST FAIL ($fail)"; exit 1
