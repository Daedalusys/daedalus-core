#!/usr/bin/env bash
# 镜像层内插件目录位门禁:每个插件安装态目录在它**首次出现的那一层**必须已是 0755。
#
# 为什么判据是「首层」而不是「合并视图」
#   ostree 从容器镜像导入时,对每个 OCI 层先各自成一个 commit,再用 union checkout
#   叠起来;而 union checkout 对**已存在**的目录不施加 fchmod(只在新建时设 mode),
#   合并 commit 的目录元数据又取自那次 checkout 的磁盘 stat。于是某个目录的最终
#   mode 由「首次出现那一层」决定,后续层对同一目录的 chmod 一律不生效。
#   镜像合并视图(podman run / podman export)与部署结果可以不一致 —— 曾出现的缺陷
#   正是:合并视图 0755、部署 0700,只有 copilot 一个目录异常,因为它是唯一 mode
#   在层间变过的目录。
#
# 因此本门禁逐层扫 tar 条目,取每个插件目录的**首现 mode**,要求 0755。
#
# 用法:
#   plugin-install-mode-image.sh [IMAGE_REF]             # 需 podman + 已构建镜像
#   plugin-install-mode-image.sh --selftest              # 只验判定逻辑,不碰镜像
set -euo pipefail

IMAGE="${1:-localhost/daedalus-os:latest}"
[ "${1:-}" = "--selftest" ] && IMAGE=""

# check_first_seen —— 输入是 `mode<TAB>条目名` 的清单(按层序升序),
# 对每个插件安装态目录取其首现 mode,非 0755 即判失败。
# 参数:$1 = 插件父目录(如 opt/daedalus/plugins),$2 = 首现清单。
# 返回 1 若任一目录首现 mode 不是 755。
check_first_seen() {
    local parent="$1" listing="$2" bad=0
    while IFS=$'\t' read -r mode path; do
        if [ "$mode" != "755" ]; then
            echo "FAIL: 目录首现 mode 不是 0755: $mode $path" >&2
            bad=1
        fi
    done < <(printf '%s\n' "$listing" | awk -F'\t' -v p="$parent" '
        $2 ~ ("^" p "/[^/]+/?$") {
            d = $2; sub(/\/$/, "", d)
            if (!(d in seen)) { seen[d] = 1; print $1 "\t" d }
        }')
    return "$bad"
}

# ---------------------------------------------------------------- 自检 ---------
if [ "${1:-}" = "--selftest" ]; then
    fail_n=0
    st() {
        if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
        else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail_n=$((fail_n + 1)); fi
    }
    printf '== plugin-install-mode-image.sh --selftest ==\n'
    P=opt/daedalus/plugins

    # 本缺陷的形态:同一目录两层都有,首层 700、次层 755 → 必须红(首层胜出)。
    L="$(printf '%s\n' \
        "700	$P/daedalus.copilot/" \
        "755	$P/daedalus.copilot/" \
        "755	$P/daedalus.fs/" \
        "755	$P/daedalus.fs/")"
    if check_first_seen "$P" "$L" >/dev/null 2>&1; then rc=0; else rc=$?; fi
    st 1 "$rc" "首层 700、次层 755 → 失败(首层决定部署后的 mode)"

    # 修好之后的形态:首层即 755,次层也 755 → 必须绿。
    L="$(printf '%s\n' \
        "755	$P/daedalus.copilot/" \
        "755	$P/daedalus.copilot/" \
        "755	$P/daedalus.i18n/")"
    if check_first_seen "$P" "$L" >/dev/null 2>&1; then rc=0; else rc=$?; fi
    st 0 "$rc" "首层即 755 → 通过"

    # 深层子目录(如插件内 i18n/)不属于本判据:只判直接子目录,避免误伤。
    L="$(printf '%s\n' "700	$P/daedalus.copilot/i18n/" "755	$P/daedalus.copilot/")"
    if check_first_seen "$P" "$L" >/dev/null 2>&1; then rc=0; else rc=$?; fi
    st 0 "$rc" "只判插件目录本身,不受插件内子目录影响"

    if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
    echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
fi

# ---------------------------------------------------------------- 正式流程 -----
command -v podman >/dev/null \
    || { echo "plugin-install-mode-image.sh: 缺 podman,无法在镜像内断言(fail-closed)" >&2; exit 1; }

printf '== 镜像层内插件目录位门禁:%s ==\n' "$IMAGE"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# podman save 出的 OCI 归档:schema 简单(index.json + blobs/sha256/*),不依赖 skopeo。
# 归档体积可达数 GB,故逐层**流式**扫读,不在磁盘上展开任何一层。
podman save --format oci-archive -o "$work/img.tar" "$IMAGE" >/dev/null

# 按层序抽出每个插件目录的 mode;同一目录多层出现时由 check_first_seen 只取首条。
# 归档本身是可寻址的普通 tar,故先按名字定位成员,再把层 blob 以流的方式喂给
# 嵌套 tarfile 解析 —— 全程不把任何一层落盘。
python3 - "$work/img.tar" >"$work/dirs.tsv" <<'PY'
import json, sys, tarfile

archive = sys.argv[1]
PLUGIN_PARENT = "opt/daedalus/plugins"

with tarfile.open(archive, "r:") as tf:
    # index.json → manifest digest;manifest → 层序与各层 digest。
    index = None
    for m in tf:
        if m.name.endswith("index.json") and m.isfile():
            index = json.load(tf.extractfile(m))
            break
    if index is None:
        sys.exit("归档里没有 index.json")

    manifest = None
    want = index["manifests"][0]["digest"].split(":")[-1]
    for m in tf:
        if m.isfile() and m.name.endswith(want):
            manifest = json.load(tf.extractfile(m))
            break
    if manifest is None:
        sys.exit("归档里找不到 manifest")

    # 层序即 manifest["layers"] 的顺序(这正是 ostree 依次 union 的顺序)。
    for layer in manifest["layers"]:
        h = layer["digest"].split(":")[-1]
        blob = None
        for m in tf:
            if m.isfile() and m.name.endswith(h):
                blob = m
                break
        if blob is None:
            sys.exit(f"归档里找不到层 blob {h}")
        # 层 blob 是 gzip 压缩的 tar;用流模式嵌套解析,避免落盘。
        f = tf.extractfile(blob)
        with tarfile.open(fileobj=f, mode="r|gz") as layer_tf:
            for entry in layer_tf:
                if not entry.isdir():
                    continue
                name = entry.name.rstrip("/")
                if name.startswith(PLUGIN_PARENT + "/") and name.count("/") == PLUGIN_PARENT.count("/") + 1:
                    print(f"{entry.mode & 0o7777:o}\t{name}")
PY

first_seen="$(awk -F'\t' '
    $2 ~ /^opt\/daedalus\/plugins\/[^/]+\/?$/ {
        d = $2; sub(/\/$/, "", d)
        if (!(d in seen)) { seen[d] = 1; print $1 "\t" d }
    }' "$work/dirs.tsv")"

if [ -z "$first_seen" ]; then
    echo "FAIL: 镜像里没找到任何插件安装态目录(层扫描瞎了?)" >&2
    exit 1
fi

echo "$first_seen" | awk -F'\t' '{ printf "  %s %s\n", $1, $2 }'
check_first_seen opt/daedalus/plugins "$first_seen" \
    || { echo "FAIL: 插件目录首现 mode 非 0755 —— 部署后普通用户读不到 manifest" >&2; exit 1; }
echo "OK: 全部插件目录首现 mode 均为 0755"
