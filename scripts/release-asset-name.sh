#!/usr/bin/env bash
# 发布资产命名的纯函数:release_asset_name <kind> <path> → `<kind>-<version>.<ext>`
#
# 版本标识与资产名一一对应,是"按这个 URL 拉"能拉对东西的前提;把它单独成函数,
# 是为了让命名规则只存在一处 —— 散进 workflow 里的名字迟早与 Release tag 漂开。
# 上游 CI 卖 Release 资产有三处硬约束,故这里必须把它们变成可断言的行为:
#   * version:有 tag 用 tag(与创建的 Release tag 同名),否则用本次 commit 的短 sha;
#   * ext:按产物后缀判定,不信调用方给的 kind(名字对不上就等于拉错文件);
#   * 2GiB:GitHub Release 单文件上限,超限退出码 2 —— 上传前拦住,绝不静默跳过。
#
# 退出码:0 成功 / 1 参数或环境缺失 / 2 产物超过 2GiB 上限。
set -uo pipefail

kind="${1:-}"
path="${2:-}"
if [ -z "$kind" ] || [ -z "$path" ]; then
    echo "用法: release_asset_name <kind> <path>" >&2
    exit 1
fi
if [ ! -f "$path" ]; then
    echo "产物不存在: $path" >&2
    exit 1
fi

# stat 而非 du:占位/稀疏产物的真实长度由字节数决定,块数会漏判上限。
size="$(stat -c %s "$path")"
if [ "$size" -gt 2147483648 ]; then
    echo "产物 ${size} 字节超过 GitHub Release 单文件上限 2GiB(2147483648)" >&2
    exit 2
fi

case "$path" in
    *.qcow2) ext=qcow2 ;;
    *.iso) ext=iso ;;
    *) ext="${path##*.}" ;;
esac

sha="${GITHUB_SHA:-}"
version="${RELEASE_TAG:-}"
if [ -z "$version" ]; then
    version="${sha:0:12}"
fi
if [ -z "$version" ]; then
    echo "RELEASE_TAG 与 GITHUB_SHA 均缺,无法定版本" >&2
    exit 1
fi

printf '%s-%s.%s\n' "$kind" "$version" "$ext"
