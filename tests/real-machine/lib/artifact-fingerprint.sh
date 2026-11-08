#!/usr/bin/env bash
# 门禁缓存产物的指纹工具。
#
# 为什么需要:自托管 runner 的 VERIFY_WORK 跨 run 持久化,而 qcow2 / 派生镜像的
# 复用判据若只看"文件在不在",就会在被测镜像已经换掉之后继续用旧产物 ——
# 于是"修复无效"的假结论会一直出现。指纹的键必须是镜像 digest,tag 会变。

# 该 ref 是否指向 registry。本地自建 tag(localhost/... 或裸名)不参与远端比对:
# 对它们 podman pull 必然失败,而它们的内容就在本地。
artifact_fp_is_remote() {
    local ref="$1"
    case "$ref" in
        localhost/*) return 1 ;;
    esac
    case "${ref%%/*}" in
        *.*|*:*) return 0 ;;   # 形如 ghcr.io / registry:5000
        *)       return 1 ;;   # 裸名 → 本地
    esac
}

# 远端 manifest digest。查不到必须显式失败:门禁无法确认新鲜度时不许替旧镜像背书。
artifact_fp_remote_digest() {
    local ref="$1" d
    command -v skopeo >/dev/null 2>&1 || {
        echo "ERROR: 缺 skopeo,无法查询 $ref 的远端 digest(装 skopeo,或设 VERIFY_OFFLINE=1 明确跳过校验)" >&2
        return 1
    }
    d="$(skopeo inspect --format '{{.Digest}}' "docker://$ref" 2>/dev/null)" || return 1
    [ -n "$d" ] || return 1
    printf '%s\n' "$d"
}

# 本地这份 ref 的 manifest digest。取不到返回空串:调用方据「空 != 期望值」判为需要拉新。
artifact_fp_local_digest() {
    podman image inspect --format '{{.Digest}}' "$1" 2>/dev/null || true
}

# 本地这份 ref 记录的所有 RepoDigests。同一个 tag 可以同时挂着两条(重推后旧的
# 那条不会消失),而 .Digest 只取其中一个 —— 只认 .Digest 会把「内容其实就是期望值」
# 的镜像误判成旧内容,进而拉一遍 2.2GB 再报错。
artifact_fp_local_repo_digests() {
    podman image inspect --format '{{.RepoDigests}}' "$1" 2>/dev/null || true
}

# 本地这份内容是否就是 expected 那版。判据取「任一 RepoDigest 或 .Digest 命中」。
artifact_fp_has_digest() {
    local ref="$1" want="$2" d
    [ "$(artifact_fp_local_digest "$ref")" = "$want" ] && return 0
    for d in $(artifact_fp_local_repo_digests "$ref" | tr -d '[],' ); do
        [ "${d#*@}" = "$want" ] && return 0
    done
    return 1
}

# 只比对 manifest digest 决定要不要拉:tag 形态不承载「内容是否变过」的信息 ——
# CI 用 :${github.sha} 也会在 workflow_dispatch 重跑时被重推成不同内容。
artifact_fp_refresh() {
    local ref="$1" remote local_digest
    if ! artifact_fp_is_remote "$ref"; then
        return 0
    fi
    if [ "${VERIFY_OFFLINE:-0}" = "1" ]; then
        echo "WARN: VERIFY_OFFLINE=1 —— 未校验 $ref 的新鲜度,可能在旧镜像上判定" >&2
        return 0
    fi
    # 构建腿把本次推出的 digest 显式传下来:有它就不必问 registry,也避免
    # 「registry 恰好还没生效」造成的短暂不一致。
    if [ -n "${VERIFY_IMAGE_DIGEST:-}" ]; then
        if ! artifact_fp_has_digest "$ref" "$VERIFY_IMAGE_DIGEST"; then
            echo "本地镜像 digest 与本次构建产出的不一致 → 拉新。ref=$ref 期望=$VERIFY_IMAGE_DIGEST 本地=$(artifact_fp_local_digest "$ref" | sed 's/^$/ <无>/')" >&2
            podman pull "$ref" >/dev/null 2>&1 || {
                echo "ERROR: 拉取 $ref 失败" >&2
                return 1
            }
            # 拉完仍对不上:registry 里这个 tag 的内容还不是本次构建的那份。
            # 继续判定就是拿别的镜像的结论冒充本次结果,故显式失败。
            if ! artifact_fp_has_digest "$ref" "$VERIFY_IMAGE_DIGEST"; then
                echo "ERROR: 拉取后 $ref 的 digest 是 $(artifact_fp_local_digest "$ref" | sed 's/^$/ <无>/'),不是本次构建产出的 $VERIFY_IMAGE_DIGEST" >&2
                return 1
            fi
        fi
        return 0
    fi
    remote="$(artifact_fp_remote_digest "$ref")" || {
        echo "ERROR: 查不到 $ref 的远端 digest,拒绝在未确认新鲜度的镜像上判定" >&2
        return 1
    }
    local_digest="$(artifact_fp_local_digest "$ref")"
    if ! artifact_fp_has_digest "$ref" "$remote"; then
        echo "镜像内容已变 → 拉新。ref=$ref 远端=$remote 本地=${local_digest:-<无>}" >&2
        podman pull "$ref" >/dev/null 2>&1 || {
            echo "ERROR: 拉取 $ref 失败" >&2
            return 1
        }
    fi
    return 0
}

artifact_fingerprint() {
    local ref="$1" id digest
    artifact_fp_refresh "$ref" || return 1
    if ! podman image exists "$ref" >/dev/null 2>&1; then
        echo "ERROR: $ref 在本地不存在且未能拉取" >&2
        return 1
    fi
    # 用 config digest(.Id)而不是 manifest digest(.Digest):
    #   同一份内容被重推一次,manifest 字节会变(annotations/压缩方式),.Digest 就变了;
    #   而实测同一个 tag 的本地镜像可以同时挂着两个 RepoDigests,.Digest 取的是其中
    #   第一个,顺序还会随重新拉取变化 —— 拿它当指纹,同一份镜像会被判成"换过"。
    #   .Id 由 config JSON 算出,config 里的 rootfs.diff_ids 覆盖了每一层内容,
    #   所以它既是内容标识、又不会因重推而漂移。本地 `podman build` 出来的镜像只有
    #   .Id,没有 RepoDigests —— 用 .Id 也就顺带把那条路径救回来了。
    id="$(podman image inspect --format '{{.Id}}' "$ref" 2>/dev/null)"
    if [ -n "$id" ] && [ "$id" != "<none>" ]; then
        printf '%s\n' "$id"
        return 0
    fi
    digest="$(podman image inspect --format '{{.Digest}}' "$ref" 2>/dev/null)"
    # 两个都取不到必须显式失败:空指纹会让所有后续比对都"匹配",正是要消灭的形态。
    if [ -z "$digest" ] || [ "$digest" = "<none>" ]; then
        echo "ERROR: 取不到 $ref 的指纹(镜像可能已被 prune)" >&2
        return 1
    fi
    printf '%s\n' "$digest"
}

artifact_fp_write() {
    local fpfile="$1" fp="$2"
    printf '%s\n' "$fp" > "$fpfile.tmp" && mv "$fpfile.tmp" "$fpfile"
}

artifact_fp_match() {
    local fpfile="$1" fp="$2" have
    [ -s "$fpfile" ] || return 1
    have="$(cat "$fpfile" 2>/dev/null)"
    [ -n "$have" ] || return 1
    [ "$have" = "$fp" ]
}

artifact_fp_describe() {
    local fpfile="$1"
    if [ -s "$fpfile" ]; then cat "$fpfile"; else printf '<无>\n'; fi
}

# 删掉指纹:决定重建时必须先删,否则构建中途失败留下的半成品会在下一轮
# 被判成"指纹一致"而复用,并被盖上本次镜像的指纹。
artifact_fp_remove() {
    local fpfile="$1"
    rm -f "$fpfile"
}

# 可变 tag 判定:没有 tag(裸 digest 形态)或 tag 是 latest/main/分支名之类都算。
# 只回答「这个 tag 名字是否浮动」,不回答「内容有没有变过」——
# 固定形态的 tag 同样会被重推成不同内容(CI 用 :<commit sha> 也会在重跑时重推),
# 故新鲜度判定一律走 artifact_fp_refresh 的远端 digest 比对。
artifact_fp_ref_is_mutable() {
    local ref="$1" tag
    case "$ref" in
        *@sha256:*) return 1 ;;                       # 直接钉 digest,不可变
    esac
    case "$ref" in
        *:*) tag="${ref##*:}" ;;
        *)   return 0 ;;                              # 没有 tag,按可变处理
    esac
    # 去掉 registry 端口造成的误判:host:5000/img 里的 5000 不是 tag。
    case "$tag" in
        */*) return 0 ;;
    esac
    case "$tag" in
        latest|main|master|stable|edge|dev|develop|nightly|rolling) return 0 ;;
        v[0-9]*) return 1 ;;                          # 版本 tag
        [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) return 1 ;;  # commit sha
    esac
    return 0
}
