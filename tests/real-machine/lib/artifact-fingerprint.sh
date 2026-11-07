#!/usr/bin/env bash
# 门禁缓存产物的指纹工具。
#
# 为什么需要:自托管 runner 的 VERIFY_WORK 跨 run 持久化,而 qcow2 / 派生镜像的
# 复用判据若只看"文件在不在",就会在被测镜像已经换掉之后继续用旧产物 ——
# 于是"修复无效"的假结论会一直出现。指纹的键必须是镜像 digest,tag 会变。

artifact_fingerprint() {
    local ref="$1" digest
    if ! podman image exists "$ref" >/dev/null 2>&1; then
        podman pull "$ref" >/dev/null 2>&1 || {
            echo "ERROR: 拉取 $ref 失败,无法取指纹" >&2
            return 1
        }
    fi
    digest="$(podman image inspect --format '{{.Digest}}' "$ref" 2>/dev/null)"
    # 取不到 digest 必须显式失败:空指纹会让所有后续比对都"匹配",正是要消灭的形态。
    if [ -z "$digest" ] || [ "$digest" = "<none>" ]; then
        echo "ERROR: 取不到 $ref 的 digest(镜像可能已被 prune)" >&2
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
