#!/usr/bin/env bash
# local-cross-repo-test.sh: 本地等价全链集成测试(无 gh / 无网络兜底)。
#
# 与 V3 构建机/CI 的跨仓 release 流水线等价,全链 4 步:
#   1) 本地构建 6 插件二进制 + daedalus-plugin-pack 打 6 个 zip;
#   2) fetch-plugins.sh --local-zip-dir 解压(校验)到临时目录(不污染镜像树安装态);
#   3) daedalus-host -dir list 期望 6 插件全 ok(含 checksums 完整性);
#   4) daedalus-plugin-pack -verify 正向全过 + 篡改检测(篡改 zip 必须报
#      checksum 不匹配,检测失效即 exit 1)。
#
# 依赖: go(构建二进制);daedalus-core/bin/{daedalus-host,daedalus-plugin-pack}
# (缺失时本脚本自动构建)。
set -euo pipefail

# 解析脚本真实路径(防 /home/lofibass/code → /var/lofibass_ssd/code symlink 陷阱)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"

# 定位 daedalus-core 仓根:兼容两种落位——
#   mono-repo 布局:脚本在 <root>/daedalus-core/scripts/,仓根 = SCRIPT_DIR/../..
#   post-split 布局:脚本在 <core 仓根>/scripts/,仓根 = SCRIPT_DIR/..
if [ -f "${SCRIPT_DIR}/../../daedalus-core/go.mod" ]; then
    CORE_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd -P)/daedalus-core"
elif [ -f "${SCRIPT_DIR}/../go.mod" ]; then
    CORE_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
else
    echo "错误: 无法定位 daedalus-core 仓根(go.mod 未找到)" >&2
    exit 1
fi
# 插件仓 = core 仓的平级兄弟(mono-repo 与 post-split 两种布局下均成立)
PLUGINS_ROOT="${CORE_ROOT}/../daedalus-plugins"
if [ ! -d "${PLUGINS_ROOT}/fs" ]; then
    echo "错误: 找不到插件仓 ${PLUGINS_ROOT}(期望 6 个 sub-module 平级就位)" >&2
    exit 1
fi

# go 多源兜底(SSH 远端非交互 shell 不 source rc,asdf 用户的 go 不在 PATH)
if ! command -v go >/dev/null 2>&1; then
    for c in "$HOME/.asdf/shims/go" "$HOME/.local/bin/go" /usr/local/go/bin/go /usr/lib/go/bin/go; do
        if [ -x "$c" ]; then
            export PATH="$(dirname "$c"):$PATH"
            break
        fi
    done
fi
command -v go >/dev/null || { echo "错误: PATH 与兜底位置均找不到 go" >&2; exit 1; }

# 确保宿主与打包器二进制就位(缺失即构建)
CORE_BIN="${CORE_ROOT}/bin"
mkdir -p "${CORE_BIN}"
for b in daedalus-host daedalus-plugin-pack; do
    if [ ! -x "${CORE_BIN}/${b}" ]; then
        echo "== 构建缺失二进制 ${b}"
        (cd "${CORE_ROOT}" && CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -o "bin/${b}" "./cmd/${b}")
    fi
done
export PATH="${CORE_BIN}:${PATH}"

# 临时目录(不污染镜像树安装态)
ZIP_DIR="/tmp/test-zips/bin"
DEST="/tmp/plugins-install"
mkdir -p "${ZIP_DIR}" "${DEST}"
find "${ZIP_DIR}" -mindepth 1 -delete
find "${DEST}" -mindepth 1 -delete

# 1) 本地生成 6 zip:构建二进制 + 打包。打包 -in . 要求插件源目录 bin/ 有与
#    manifest executable 匹配的二进制;把新构建的二进制拷入,保证 zip 内容
#    新鲜且脚本在全新 clone(无 bin/)下自洽。
for cap in fs shell pkg sysinfo service blueprint dupe trace proc; do
    echo "== 构建 + 打包 ${cap}"
    (cd "${PLUGINS_ROOT}/${cap}" && go build -trimpath -o "${ZIP_DIR}/daedalus-${cap}" "./cmd/daedalus-${cap}")
    cp -f "${ZIP_DIR}/daedalus-${cap}" "${PLUGINS_ROOT}/${cap}/bin/daedalus-${cap}"
    chmod 0755 "${PLUGINS_ROOT}/${cap}/bin/daedalus-${cap}"
    (cd "${PLUGINS_ROOT}/${cap}" && daedalus-plugin-pack -in . -out "${ZIP_DIR}/daedalus.${cap}.plugin.zip")
done

# 2) fetch-plugins.sh --local-zip-dir 解压(校验)到临时目录
echo "== fetch-plugins.sh --local-zip-dir ${ZIP_DIR} --dest ${DEST}"
bash "${SCRIPT_DIR}/fetch-plugins.sh" --local-zip-dir "${ZIP_DIR}" --dest "${DEST}"

# 3) daedalus-host -dir list:期望 6 插件全 ok(含 checksums 完整性)
echo "== daedalus-host -dir ${DEST} list"
list_out="$("${CORE_BIN}/daedalus-host" -dir "${DEST}" list)"
echo "${list_out}"
ok_count="$(echo "${list_out}" | awk 'NR>1 {print $NF}' | grep -c '^ok$' || true)"
deg_count="$(echo "${list_out}" | awk 'NR>1 {print $NF}' | grep -c '^degraded$' || true)"
if [ "${ok_count}" -ne 6 ] || [ "${deg_count}" -ne 0 ]; then
    echo "错误: 期望 6 插件全 ok,实际 ok=${ok_count} degraded=${deg_count}" >&2
    exit 1
fi

# 4a) 正向校验:6 zip 逐一 -verify 必须全过(任一 checksum 不匹配 → exit 1)
echo "== 正向校验:6 zip 逐一 -verify"
for zip in "${ZIP_DIR}"/*.plugin.zip; do
    echo "verify: $(basename "${zip}")"
    "${CORE_BIN}/daedalus-plugin-pack" -verify "${zip}" >/dev/null
done

# 4b) 篡改检测:复制一个 zip,篡改包内二进制字节后重新打包,-verify 必须报
#     checksum 不匹配(exit != 0);若校验通过则检测失效 → exit 1
echo "== 篡改检测"
TAMPER_DIR="/tmp/test-zips/tampered"
mkdir -p "${TAMPER_DIR}"
find "${TAMPER_DIR}" -mindepth 1 -delete
tampered="${TAMPER_DIR}/daedalus.fs.tampered.plugin.zip"
python3 - "${ZIP_DIR}/daedalus.fs.plugin.zip" "${tampered}" <<'PYEOF'
import sys, zipfile
src, dst = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(src) as zin, zipfile.ZipFile(dst, "w") as zout:
    for item in zin.infolist():
        data = zin.read(item.filename)
        if item.filename == "bin/daedalus-fs":
            data += b"\x00"  # 篡改:给二进制追加一个字节,checksum 必然不匹配
        zout.writestr(item, data)
PYEOF
if "${CORE_BIN}/daedalus-plugin-pack" -verify "${tampered}" >/tmp/test-zips/tamper.out 2>&1; then
    echo "错误: 篡改后的 zip 校验竟然通过,checksum 检测失效" >&2
    exit 1
fi
if ! grep -q "checksum" /tmp/test-zips/tamper.out; then
    echo "错误: 篡改后的 zip 校验失败但未报 checksum 不匹配(输出: $(cat /tmp/test-zips/tamper.out))" >&2
    exit 1
fi
echo "篡改检测通过: $(cat /tmp/test-zips/tamper.out)"

echo ""
echo "PASS: 本地跨仓全链测试通过(6 zip 生成 + fetch 解压校验 + host list 6 ok + checksum 校验/篡改检测)"