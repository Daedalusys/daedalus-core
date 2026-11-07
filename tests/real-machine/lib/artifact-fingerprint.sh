#!/usr/bin/env bash
# 门禁缓存产物的指纹工具。
#
# 为什么需要:自托管 runner 的 VERIFY_WORK 跨 run 持久化,而 qcow2 / 派生镜像的
# 复用判据若只看"文件在不在",就会在被测镜像已经换掉之后继续用旧产物 ——
# 于是"修复无效"的假结论会一直出现。指纹的键必须是镜像 digest,tag 会变。

artifact_fingerprint() {
    local ref="$1" id digest
    if ! podman image exists "$ref" >/dev/null 2>&1; then
        podman pull "$ref" >/dev/null 2>&1 || {
            echo "ERROR: 拉取 $ref 失败,无法取指纹" >&2
            return 1
        }
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
