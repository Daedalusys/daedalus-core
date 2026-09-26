# demo/dev 侧 recipe(go-build-demo / dev-copilot / cli-copilot)拆分到
# scripts/justfile.demo,经 just import 合入同一命名空间,`just --list`
# 自动发现;import 必须出现在所有 recipe 之前。
import "scripts/justfile.demo"

# 本机私有 recipe(ThinkPadTest 的 KVM install / RDP / debug 等)。
# justfile.local 在 .gitignore(永不进库),本地存在时自动合入,直接
# `just deploy` / `just kvm-install` / `just print-rdp` 即可,不用 -f。
# CI runner 上 clone 出来无此文件 → just 解析期 fail,故 CI workflow 在
# actions/checkout 之后加 `touch justfile.local` 一步兜底(空 import 等价
# 无操作,主 recipe 全可用;本地 recipe 在 CI 自然不生效,符合预期)。
import "justfile.local"

# 自动加载仓库根 .env(SSH/本机用同一份配置,gitignored 不进库)。模板见
# .env.example;未提供 .env 时不报错(loaded 静默忽略)。
set dotenv-load

# Default recipe: list available recipes
default:
    @just --list

# Sync Daedalus-owned files into base_image tree
sync:
    ./scripts/sync-daedalus.sh

# 跨仓 release 流水线:从 daedalus-plugins 仓 GitHub release 拉 6 个
# *.plugin.zip 并解压(校验)到镜像树安装态;本机无 gh/无网络时用
# --local-zip-dir 指向本地 zip 目录兜底(见 scripts/fetch-plugins.sh --help)。
# 依赖 daedalus-core/bin/daedalus-plugin-pack(先 just go-build / plugin-pack)。
fetch-plugins:
    #!/usr/bin/env bash
    set -euo pipefail
    # 规范 cd 形态(与 go-build/go-test/plugin-pack 同款):CI 的 run 步在
    # daedalus-core 子目录内调 just,旧写法 `bash daedalus-core/scripts/...`
    # 假设 workspace 根 cwd,拆仓后必挂(exit 127);脚本自带 CORE_ROOT 定位,直接相对调用。
    root="$(cd .. && pwd)"
    cd "$root/daedalus-core"
    # 幂等衔接:CI build-image job 已把 release zip 下到 /tmp/plugins-release,
    # 脚本默认腿 gh release download 拒覆盖已存在文件(rc=1)→ 经
    # DAEDALUS_FETCH_ZIP_DIR 环境变量把已就位目录以 --local-zip-dir 传入,
    # 跳过重复下载、保留解压即校验(fail-closed);本机不设该变量,走 gh 默认腿。
    zipdir="${DAEDALUS_FETCH_ZIP_DIR:-}"
    if [ -n "$zipdir" ]; then
        bash scripts/fetch-plugins.sh --local-zip-dir "$zipdir"
    else
        bash scripts/fetch-plugins.sh
    fi

# Build Daedalus container image
# --network=host:让容器共享 host 网络栈,容器内 127.0.0.1 才指 host(用 host 的
#   127.0.0.1:7890 proxy);默认 podman 用 slirp4netns,127.0.0.1 指容器自己。
# --env HTTP_PROXY / --env HTTPS_PROXY:把 host 的 env 透传给容器,77-xrdp.sh
#   之类 dnf 调用能拿到(若 host 没设,容器里就是空,dnf 直连不变)。
# --cache-from 故意不传:本机 buildkit 版本对 --cache-from=localhost/...:tag 解析
#   有问题(报 "repository must contain neither a tag nor digest"),放弃跨 build
#   镜像层复用。Containerfile 已拆 8 stage + dnf cache mount,单次 build 内
#   已经够快;第二次 build 想更快就手动 `podman pull localhost/daedalus-os:latest`
#   后再跑,或后续升级 buildkit 再加回。
# --jobs:BuildKit 内部独立步骤并行(对单步骤 RUN 帮助有限,加上零成本)。
# 这几行**不影响 CI**(github-actions runner 上 HTTP_PROXY 不设,dnf 直连仓库不受影响)。
# 凭据透传:DAEDALUS_DEV_USER/PASS 从环境或 .env 取,空值时 78 脚本走公开构建模式(不注入账号)
# 依赖顺序:fetch-plugins(拉插件 zip 解压到安装态)→ sync(安装态同步进 base_image)→ podman build
build: fetch-plugins sync
    podman build --jobs=$(nproc) --network=host --env HTTP_PROXY --env HTTPS_PROXY --env DAEDALUS_DEV_USER="{{ env_var_or_default('DAEDALUS_DEV_USER', '') }}" --env DAEDALUS_DEV_PASS="{{ env_var_or_default('DAEDALUS_DEV_PASS', '') }}" --platform=linux/amd64 --security-opt=label=disable --cap-add=all --device /dev/fuse --build-arg IMAGE_NAME=daedalus-os --build-arg IMAGE_REGISTRY=localhost --build-arg VARIANT=kde -t localhost/daedalus-os:latest -f Containerfile .

# --no-cache 版 build:改 build.sh / 脚本内容后必须用它。
# 原因:脚本经 --mount=type=bind,from=ctx 进 RUN,bind-mount 内容不进 Buildah
# 缓存 key → 改了脚本、甚至改了 build.sh 的去重 bug,stage RUN 仍 "Using cache"
# 复用旧层(镜像一直没 KDE 就是这个坑)。要真重跑改 build.sh 影响的 stage 就 --no-cache。
# 凭据透传:DAEDALUS_DEV_USER/PASS 从环境或 .env 取,空值时 78 脚本走公开构建模式(不注入账号)
# 依赖顺序同 build:fetch-plugins → sync → podman build
build-nocache: fetch-plugins sync
    podman build --no-cache --jobs=$(nproc) --network=host --env HTTP_PROXY --env HTTPS_PROXY --env DAEDALUS_DEV_USER="{{ env_var_or_default('DAEDALUS_DEV_USER', '') }}" --env DAEDALUS_DEV_PASS="{{ env_var_or_default('DAEDALUS_DEV_PASS', '') }}" --platform=linux/amd64 --security-opt=label=disable --cap-add=all --device /dev/fuse --build-arg IMAGE_NAME=daedalus-os --build-arg IMAGE_REGISTRY=localhost --build-arg VARIANT=kde -t localhost/daedalus-os:latest -f Containerfile .

# 镜像零残留断言(仅在 just build 成功后可跑):
# 断言镜像 /opt 内无 Python 源码/字节码、__pycache__、Deno 测试文件、Go 模块/依赖残留。
# 任一新布局构建产物泄漏进 rootfs 即 exit 1;干净时打印 OK。
verify-image:
    #!/usr/bin/env bash
    set -euo pipefail
    podman run --rm localhost/daedalus-os:latest sh -c 'find /opt \( -name "*.py" -o -name "*.pyc" -o -name "__pycache__" -o -name "*.test.ts" -o -name "go.mod" -o -name vendor \) | grep . && exit 1 || echo OK'

# 3 仓平级布局守门:检查 daedalus-sdk / daedalus-plugins 兄弟仓
# 是否以平级目录形态就位(go.work 本地 dev 桥的前置);缺哪个报哪个,exit 1。
verify-dev-layout:
    #!/usr/bin/env bash
    set -euo pipefail
    # 同款规范 cd 形态:旧写法假设 workspace 根 cwd(拆仓前形态);脚本自带
    # SCRIPT_DIR→CORE_ROOT 定位,从仓根相对调用即可。
    root="$(cd .. && pwd)"
    cd "$root/daedalus-core"
    bash scripts/verify-dev-layout.sh

# 打包 8 个能力插件(fs/shell/pkg/sysinfo/service/blueprint/dupe/trace)为 daedalus-plugin
# (构建镜像前执行)
# 同时安装带外 CLI 到 /usr/local/bin: host/audit/tx(本仓现构)+ shell/service
# (插件仓现构;3 仓拆分后能力二进制出厂源已从 core 迁至 daedalus-plugins)
plugin-pack: blueprint-embed
    #!/usr/bin/env bash
    set -euo pipefail
    # 与 go-build / go-test 同款 fallback:SSH 远端非交互 shell 不 source rc,asdf
    # 用户的 go 不在 PATH,recipe 自带多源兜底(asdf → .local → 系统 go)。这里
    # 必须自带,因为 plugin-pack 是独立 recipe 不依赖 go-build 已跑过(可能 go-build
    # 用 apt 装 go 跳过了 fallback,而本机是 asdf 装的)。
    if ! command -v go >/dev/null 2>&1; then
        for c in "$HOME/.asdf/shims/go" "$HOME/.local/bin/go" /usr/local/go/bin/go /usr/lib/go/bin/go; do
            if [ -x "$c" ]; then
                export PATH="$(dirname "$c"):$PATH"
                break
            fi
        done
    fi
    command -v go >/dev/null || { echo "ERROR: go not found in PATH or any fallback"; exit 1; }
    # 流程(构建期内建,无运行时安装;安装态必经 Pack 注入 checksums):
    #   1) 本仓构建 5 个 core runtime 静态二进制(host/audit/tx/smoke/plugin-pack);
    #   2) 能力二进制在兄弟仓 daedalus-plugins/<cap> 模块内就地构建(唯一事实源),
    #      产物写 <cap>/bin/,输出路径取 manifest executable 字段;
    #   3) 暂存目录只放 daedalus.plugin.json + bin/(zip 不含源码不变式)→ plugin-pack
    #      -in/-out 打 zip 进 core bin/:Pack 注入逐条目 sha256 checksums + manifest 规范化自摘要;
    #   4) plugin-pack -verify --keep 把 zip 解压到镜像树安装态目录——解压即完整校验,
    #      任一摘要不符拒绝安装;安装态经 ./scripts/sync-daedalus.sh 同步为镜像 /opt/daedalus/plugins。
    root="$(cd .. && pwd)"
    cd "$root/daedalus-core"
    CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -o bin/ ./cmd/...
    # 插件仓每个 cap 的 go.mod 都有 `replace ... => ../daedalus-sdk`:相对 cap 目录即
    # daedalus-plugins/daedalus-sdk,该路径只在插件仓自身 CI workspace 才存在;本机三仓
    # 平级布局没有它,GOWORK=off 单模块直接构建必失败。方案:临时把
    # daedalus-plugins/daedalus-sdk 软链到平级 ../daedalus-sdk 充当桥,能力循环无论成败
    # 由 trap EXIT 立即移除。该软链路径在插件仓【不】被 gitignore,绝不许留残;
    # 若已存在则尊重原样:不接管也不删除;平级 daedalus-sdk 缺失则 fail-closed 拒构。
    sdk_stub="$root/daedalus-plugins/daedalus-sdk"
    sdk_stub_created=0
    if [ ! -e "${sdk_stub}" ] && [ ! -L "${sdk_stub}" ]; then
        if [ ! -d "$root/daedalus-sdk" ]; then
            echo "ERROR: 平级 daedalus-sdk 不存在,无法满足 daedalus-plugins/*/go.mod 的 replace(=> ../daedalus-sdk);拒绝继续" >&2
            exit 1
        fi
        ln -sfn ../daedalus-sdk "${sdk_stub}"
        sdk_stub_created=1
    fi
    cleanup_sdk_stub() {
        # 只删本 recipe 自己创建的那个软链;unlink 而非 rm(语义最小,rm 在本机权限面有约束史)。
        if [ "${sdk_stub_created}" = 1 ] && [ -L "${sdk_stub}" ]; then
            unlink "${sdk_stub}"
        fi
    }
    trap cleanup_sdk_stub EXIT
    # 安装态 plugins/daedalus.<cap>/ 与 70 编号脚本"仅 chmod+提示"分工互补——装配职责恒在
    # 本 recipe。全部插件统一经暂存目录打包:源目录含 cmd/ 源码 + go.mod + go.sum +
    # testdata,直接 -in 源目录会把源码打进 zip、解压后泄漏源码,违反零残留断言。
    # 暂存目录只放 daedalus.plugin.json + ${exe};blueprint 的 blueprints/ 蓝图数据已
    # //go:embed 进二进制,同样排除,避免 rootfs 出现重复蓝图数据。
    stage="${TMPDIR:-/tmp}/daedalus-plugin-pack-stage"
    find "${stage}" -mindepth 1 -delete 2>/dev/null || true
    declare -A cap_bin   # cap → 插件仓现构产物绝对路径,供 /usr/local/bin 双落位腿复用
    for cap in fs shell pkg sysinfo service blueprint dupe trace; do
        id="daedalus.${cap}"
        src="$root/daedalus-plugins/${cap}"
        manifest="${src}/daedalus.plugin.json"
        # executable 字段(bin/daedalus-<cap>)用 sed 抽取:纯 POSIX 工具面,不引入 jq 依赖;
        # 缺字段或不在 bin/ 下都立即报错退出,绝不静默挑错文件(防 zip 内容与 manifest 漂移)。
        exe="$(sed -n 's/.*"executable"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}" | head -n 1)"
        if [ -z "${exe}" ]; then
            echo "ERROR: ${manifest} 缺少 executable 字段,无法确定产物名" >&2
            exit 1
        fi
        case "${exe}" in
            bin/*) ;;
            *) echo "ERROR: ${manifest} 的 executable 必须以 bin/ 相对路径声明,实际: ${exe}" >&2; exit 1 ;;
        esac
        mkdir -p "${src}/bin"
        # 在 cap 模块目录内构建(插件仓 = 能力服务器唯一事实源);GOWORK=off 强制单模块
        # 语义,与 core 侧 go-build 逐字同旗标:CGO_ENABLED=0 / GOTOOLCHAIN=local / -trimpath。
        # -o 直指 manifest 声明的产物路径;./cmd/... 命中多个 main 包时 go 自行报错(fail-loud)。
        ( cd "${src}" && GOWORK=off CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -o "${src}/${exe}" ./cmd/... )
        chmod 0755 "${src}/${exe}"
        cap_bin["${cap}"]="${src}/${exe}"
        # 暂存:只放 manifest + bin 产物,然后打 zip 进 core bin/(zip 不含源码不变式)。
        # find 带 || true:首循环 stage 尚不存在(preamble 清空容忍缺失),目录不存在即无需清空。
        find "${stage}" -mindepth 1 -delete 2>/dev/null || true
        mkdir -p "${stage}/$(dirname "${exe}")"
        cp -f "${manifest}" "${stage}/daedalus.plugin.json"
        cp -f "${src}/${exe}" "${stage}/${exe}"
        chmod 0755 "${stage}/${exe}"
        "./bin/daedalus-plugin-pack" -in "${stage}" -out "bin/${id}.plugin.zip"
        if [ "${cap}" = "dupe" ] || [ "${cap}" = "trace" ]; then
            # dupe/trace 仅 TMPDIR 解包校验、不进 files/system,理由(zip 照常产出到 core bin/):
            #   1) 两者的安装态从未入库(git ls-files 中 daedalus.dupe/、daedalus.trace/ 零条目),
            #      往入库树写入是净新增未跟踪残留;
            #      2) CI 镜像供料腿 fetch-plugins.sh 只解 6 个 zip、不含 dupe/trace;
            #   3) 校验语义不降级:zip 仍经 -verify --keep 解压到临时空目录,解压即完整校验、
            #      fail-closed。正式入库(安装态 commit + release + fetch)时删掉此分支即可。
            vdir="${TMPDIR:-/tmp}/daedalus-plugin-pack-verify/${id}"
            mkdir -p "${vdir}"
            find "${vdir}" -mindepth 1 -delete 2>/dev/null || true
            "./bin/daedalus-plugin-pack" -verify "bin/${id}.plugin.zip" --keep "${vdir}"
        else
            dest="$root/daedalus-core/files/system/opt/daedalus/plugins/${id}"
            mkdir -p "${dest}"
            # 解压器要求空目录(O_EXCL 不覆盖既有文件);本机权限面禁 rm,用 find -delete 清空。
            find "${dest}" -mindepth 1 -delete
            "./bin/daedalus-plugin-pack" -verify "bin/${id}.plugin.zip" --keep "${dest}"
        fi
    done
    # 宿主自身进镜像树 /usr/local/bin:构建期 76-daedalus-plugin-gen.sh 与 copilot wrapper(任务 8)都依赖它。
    install -Dm0755 "bin/daedalus-host" "$root/daedalus-core/files/system/usr/local/bin/daedalus-host"
    # copilot 运行期依赖的两个二进制同入 /usr/local/bin:audit.ts 生产默认路径 =
    # /usr/local/bin/daedalus-audit(exec.ts 同理 = daedalus-shell),wrapper 的 deno
    # --allow-run 旗标已放行这两个路径。插件安装态 plugins/daedalus.shell/bin/ 内的副本
    # 保持不动——systemd 单元仍经 76 脚本 render-unit 指向插件内二进制;此处副本仅服务
    # copilot 进程内 spawn 的字面路径。
    install -Dm0755 "bin/daedalus-audit" "$root/daedalus-core/files/system/usr/local/bin/daedalus-audit"
    # shell/service 双落位:core 不自产这两个二进制,此处拷上方能力循环在插件仓现构的
    # 产物——与插件安装态 plugins/daedalus.<cap>/bin/ 内副本逐字节同源。
    install -Dm0755 "${cap_bin[shell]}" "$root/daedalus-core/files/system/usr/local/bin/daedalus-shell"
    # daedalus-service 同款双落位:插件安装态(能力循环产出)供 systemd ExecStart(76 脚本
    # render-unit 指向);/usr/local/bin 副本服务镜像内以字面路径直接启动该二进制的 QA
    # 链路。两态互不替代。
    install -Dm0755 "${cap_bin[service]}" "$root/daedalus-core/files/system/usr/local/bin/daedalus-service"
    # daedalus-tx 为 out-of-band CLI(无插件清单、无 systemd 单元、不进 76 脚本
    #   render/handshake 环路)——与 audit 同款仅落 /usr/local/bin,服务 copilot spawn
    #   与用户直接 CLI(v1 执行模型 = 调用者进程)。
    install -Dm0755 "bin/daedalus-tx" "$root/daedalus-core/files/system/usr/local/bin/daedalus-tx"
    echo "plugin-pack: 8 个能力插件 zip(fs/shell/pkg/sysinfo/service/blueprint/dupe/trace)-> daedalus-core/bin/;6 个已解包校验安装 -> daedalus-core/files/system/opt/daedalus/plugins/(dupe/trace 仅 TMPDIR 解包校验,理由见循环内注释);host/audit/tx(core 现构)+ shell/service(插件仓现构)-> daedalus-core/files/system/usr/local/bin/"

# 开发态本地安装:把 dev 产物装进用户前缀,免镜像即可使用全套 CLI。
# 用法: just dev-install [前缀] (亦兼容 --prefix=X 形式);默认前缀 = $HOME/.local。
# 产物: <prefix>/bin/{daedalus-host,daedalus-audit,daedalus-shell}
#       <prefix>/share/daedalus/plugins/{5 个插件安装态} (消费 daedalus-core/bin/*.plugin.zip,不重新打包)
dev-install prefix='': blueprint-embed
    #!/usr/bin/env bash
    set -euo pipefail
    # 与 go-build / go-test / plugin-pack 同款 fallback:SSH 远端非交互 shell 不 source rc,
    # asdf 用户的 go 不在 PATH,recipe 自带多源兜底。
    if ! command -v go >/dev/null 2>&1; then
        for c in "$HOME/.asdf/shims/go" "$HOME/.local/bin/go" /usr/local/go/bin/go /usr/lib/go/bin/go; do
            if [ -x "$c" ]; then
                export PATH="$(dirname "$c"):$PATH"
                break
            fi
        done
    fi
    command -v go >/dev/null || { echo "ERROR: go not found in PATH or any fallback"; exit 1; }
    # 本机 just(1.58)不把位置参数透传为 $1,经 {{prefix}} 插值取参;兼容 --prefix=X 旗标形式
    prefix="{{prefix}}"
    case "${prefix}" in
        --prefix=*) prefix="${prefix#--prefix=}" ;;
    esac
    if [ -z "${prefix}" ]; then
        prefix="${HOME}/.local"
    fi
    root="$(cd .. && pwd)"
    plugins_root="${prefix}/share/daedalus/plugins"
    # sudo 判定:受保护前缀(/opt、/usr/local、/usr)且非 root 时前缀安装命令;
    # 非交互环境 sudo 不可用会立即显式失败,绝不静默半装。
    sudo=""
    case "${prefix}" in
        /opt|/opt/*|/usr/local|/usr/local/*|/usr|/usr/*)
            if [ "$(id -u)" -ne 0 ]; then
                sudo="sudo"
            fi
            ;;
    esac
    ${sudo} install -d "${prefix}/bin" "${prefix}/share/daedalus"
    # 复用 go-build/plugin-pack 逐字同旗标,不发明新构建形态。
    cd "$root/daedalus-core"
    CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -o bin/ ./cmd/...
    # 三个 CLI 二进制进 <prefix>/bin(copilot audit.ts/exec.ts 与 host 的生产同名路径)
    ${sudo} install -Dm0755 bin/daedalus-host "${prefix}/bin/daedalus-host"
    ${sudo} install -Dm0755 bin/daedalus-audit "${prefix}/bin/daedalus-audit"
    ${sudo} install -Dm0755 bin/daedalus-shell "${prefix}/bin/daedalus-shell"
    # 逐插件解压安装态;zip 缺失只 WARN 跳过,不让整个 recipe 失败
    for id in daedalus.fs daedalus.shell daedalus.pkg daedalus.sysinfo daedalus.copilot; do
        zip="bin/${id}.plugin.zip"
        dest="${plugins_root}/${id}"
        if [ ! -f "${zip}" ]; then
            echo "WARN: ${zip} 不存在,跳过 ${id}(先跑 just plugin-pack / just copilot-plugin 再重试)" >&2
            continue
        fi
        ${sudo} install -d "${dest}"
        # 解压器要求空目录(O_EXCL 不覆盖既有文件);本机权限面禁 rm,一律 find -delete 清空后再装
        if [ -n "${sudo}" ]; then
            ${sudo} find "${dest}" -mindepth 1 -delete
        else
            find "${dest}" -mindepth 1 -delete
        fi
        ${sudo} ./bin/daedalus-plugin-pack -verify "${zip}" --keep "${dest}"
    done
    echo ""
    echo "dev-install 完成: 前缀=${prefix}"
    echo "请复制以下环境变量与命令:"
    echo "  export DAEDALUS_PLUGIN_DIR=${plugins_root}"
    echo "  export PATH=${prefix}/bin:\$PATH"
    echo "  ${prefix}/bin/daedalus-host -dir ${plugins_root} list"

# 列出开发态已安装插件:优先用 <prefix>/bin 的 host,
# 不可执行则回退 PATH 中的 daedalus-host;统一 -dir 指向前缀插件目录。
host-list prefix='':
    #!/usr/bin/env bash
    set -euo pipefail
    # 本机 just(1.58)不把位置参数透传为 $1,经 {{prefix}} 插值取参;兼容 --prefix=X 旗标形式
    prefix="{{prefix}}"
    case "${prefix}" in
        --prefix=*) prefix="${prefix#--prefix=}" ;;
    esac
    if [ -z "${prefix}" ]; then
        prefix="${HOME}/.local"
    fi
    plugins_dir="${prefix}/share/daedalus/plugins"
    host="${prefix}/bin/daedalus-host"
    if [ -x "${host}" ]; then
        exec "${host}" -dir "${plugins_dir}" list
    fi
    if command -v daedalus-host >/dev/null 2>&1; then
        echo "提示: ${host} 不可执行,回退 PATH 中的 $(command -v daedalus-host)" >&2
        exec daedalus-host -dir "${plugins_dir}" list
    fi
    echo "错误: 找不到 daedalus-host(既无 ${host},也不在 PATH);请先跑 just dev-install" >&2
    exit 1

# 构建期把蓝图源码侧数据复制到 embed 目录:daedalus-plugins/blueprint/blueprints/ 在
# blueprint Go 模块之外,go:embed 不能引用 `..` 越界路径也不能跟随符号链接,故经 rsync
# 复制到模块内 cmd/daedalus-blueprint/blueprints/ 再 `//go:embed all:blueprints/*`。
# 复制产物不入库,源码侧是唯一事实源。
# 布局:root 取仓库根的父目录,兼容本地三仓平级(正常 rsync)与 CI workspace 仅检出
# core+sdk(下方守护跳过)。CI 跳过是安全的:本仓 ./cmd/... 从不编译 blueprint 模块,
# 嵌入副本仅在真正构建 daedalus-blueprint 二进制时才被消费。
# 所有会编译 Go 代码的 recipe 都以本 recipe 为依赖,保证裸 go build/test 前数据已就位。
blueprint-embed:
    #!/usr/bin/env bash
    set -euo pipefail
    root="$(cd .. && pwd)"
    # CI 布局下无 daedalus-plugins 兄弟检出 → 打 note 跳过并 exit 0,
    # 让依赖链上的 go-build / go-test 等核心 recipe 照常通过(见上方注释)。
    if [ ! -d "${root}/daedalus-plugins/blueprint/blueprints" ]; then echo "note: 未检出 daedalus-plugins 兄弟仓(CI 布局),核心 Go 构建不消费蓝图嵌入副本,跳过"; exit 0; fi
    mkdir -p "${root}/daedalus-plugins/blueprint/cmd/daedalus-blueprint/blueprints"
    rsync -a --delete "${root}/daedalus-plugins/blueprint/blueprints/" "${root}/daedalus-plugins/blueprint/cmd/daedalus-blueprint/blueprints/"

# 构建全部 Go 静态二进制到 daedalus-core/bin/:CGO_ENABLED=0 纯静态、
# GOTOOLCHAIN=local 禁用工具链自动下载、-trimpath 可复现路径。
# 用 shebang 跑(而非一行 cd && go build ...):SSH 远端非交互 shell 不 source rc,
# asdf/mise 用户装的 go 不在 PATH。先 `command -v go` 试 PATH,找不到时按常见位置
# fallback(asdf shims / $HOME/.local/bin / /usr/local/go/bin),把找到的 go 所在
# 目录 prepend 到 PATH —— 覆盖本地交互 shell、 Actions apt go、SSH asdf、SSH 裸 go。
go-build: blueprint-embed
    #!/usr/bin/env bash
    set -euo pipefail
    root="$(cd .. && pwd)"
    cd "$root/daedalus-core"
    if ! command -v go >/dev/null 2>&1; then
        for c in "$HOME/.asdf/shims/go" "$HOME/.local/bin/go" /usr/local/go/bin/go /usr/lib/go/bin/go; do
            if [ -x "$c" ]; then
                export PATH="$(dirname "$c"):$PATH"
                break
            fi
        done
    fi
    command -v go >/dev/null || { echo "ERROR: go not found in PATH or any fallback"; exit 1; }
    CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -o bin/ ./cmd/...

# Go 全量单元测试(纯模块级,不依赖镜像;just test 的 Go 腿即此命令)
# 与 go-build 同款 fallback:SSH 远端非交互 shell 不 source rc,asdf/mise
# 用户的 go 不在 PATH,recipe 自带多源兜底(asdf → .local → 系统 go)。
go-test: blueprint-embed
    #!/usr/bin/env bash
    set -euo pipefail
    root="$(cd .. && pwd)"
    cd "$root/daedalus-core"
    if ! command -v go >/dev/null 2>&1; then
        for c in "$HOME/.asdf/shims/go" "$HOME/.local/bin/go" /usr/local/go/bin/go /usr/lib/go/bin/go; do
            if [ -x "$c" ]; then
                export PATH="$(dirname "$c"):$PATH"
                break
            fi
        done
    fi
    command -v go >/dev/null || { echo "ERROR: go not found in PATH or any fallback"; exit 1; }
    go test ./...

# 显式下载 Go 模块依赖到 GOMODCACHE(vendor/ 不入库,首次构建需联网;`go build` 也会
# 隐式触发,此 recipe 仅用于预热缓存或排查模块网络问题)
# 与 go-build 同款 fallback。
deps:
    #!/usr/bin/env bash
    set -euo pipefail
    root="$(cd .. && pwd)"
    cd "$root/daedalus-core"
    if ! command -v go >/dev/null 2>&1; then
        for c in "$HOME/.asdf/shims/go" "$HOME/.local/bin/go" /usr/local/go/bin/go /usr/lib/go/bin/go; do
            if [ -x "$c" ]; then
                export PATH="$(dirname "$c"):$PATH"
                break
            fi
        done
    fi
    command -v go >/dev/null || { echo "ERROR: go not found in PATH or any fallback"; exit 1; }
    go mod download

# Run test suite(Go 腿 + Deno 腿 + i18n 键集门禁)
# go test 那行同款 fallback(SSH 远端 asdf/mise 自找 go);deno + bash 走 PATH 已有。
test: blueprint-embed
    #!/usr/bin/env bash
    set -euo pipefail
    if ! command -v go >/dev/null 2>&1; then
        for c in "$HOME/.asdf/shims/go" "$HOME/.local/bin/go" /usr/local/go/bin/go /usr/lib/go/bin/go; do
            if [ -x "$c" ]; then
                export PATH="$(dirname "$c"):$PATH"
                break
            fi
        done
    fi
    command -v go >/dev/null || { echo "ERROR: go not found in PATH or any fallback"; exit 1; }
    root="$(cd .. && pwd)"
    cd "$root/daedalus-core"
    go test ./...
    deno test --allow-all tests/deno/
    # i18n 键集门禁:en↔zh 对称 + t() 字面量双 locale 存在性
    bash tests/deno/i18n_keys.test.sh

# 打包 Copilot 为 daedalus-plugin(Deno runtime):
# 编译 pack/host → mktemp 暂存 5 个 .ts + 清单 → Pack 注入 checksums → 校验并解压安装态
# 产物: daedalus-core/bin/daedalus.copilot.plugin.zip(忽略)
#       daedalus-core/files/system/opt/daedalus/plugins/daedalus.copilot/(入库,sync 进镜像)
copilot-plugin:
    ./scripts/pack-copilot-plugin.sh

# Build bootable ISO
iso:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p output
    # save/load 交接:just build 是 rootless(镜像在 ~/.local/share/containers/storage);
    # bootc-image-builder 跑 rootful(看 /var/lib/containers/storage)。先 podman save
    # (rootless 导出)再 sudo podman load(rootful 导入),否则 builder --local 报
    # "image not known"。
    podman save --format oci-archive -o output/daedalus-os.oci.tar localhost/daedalus-os:latest
    sudo podman load -i output/daedalus-os.oci.tar
    sudo podman run --rm --privileged -v "$(pwd)/output":/output -v /var/lib/containers/storage:/var/lib/containers/storage --security-opt label=disable quay.io/centos-bootc/bootc-image-builder:latest --type iso --local localhost/daedalus-os:latest

# Build qcow2 and run in QEMU
qemu:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p output
    # save/load 交接(同 iso):rootless build 的镜像先导出再 sudo 导入 rootful 存储,
    # 否则 bootc-image-builder --local 会 "image not known"。
    podman save --format oci-archive -o output/daedalus-os.oci.tar localhost/daedalus-os:latest
    sudo podman load -i output/daedalus-os.oci.tar
    sudo podman run --rm --privileged -v "$(pwd)/output":/output -v /var/lib/containers/storage:/var/lib/containers/storage --security-opt label=disable quay.io/centos-bootc/bootc-image-builder:latest --type qcow2 --local localhost/daedalus-os:latest
    # qcow2 是磁盘镜像(不是 CDROM;-cdrom 是旧配方残留)。-enable-kvm 走 KVM 加速;
    # -netdev user + hostfwd=tcp::3389-:3389 把 guest 3389 转 host 同一端口,
    # 本机 mstsc/Remmina 直接连 localhost:3389。-display gtk 开图形窗口
    # (调试 RDP 不通时换成 -vnc :0 先 VNC 进去看)。
    qemu-system-x86_64 \
        -m 4096 -smp 2 -enable-kvm -cpu host -vga virtio \
        -drive file=output/qcow2/disk.qcow2,format=qcow2,if=virtio \
        -netdev user,id=net0,hostfwd=tcp::3389-:3389 \
        -device virtio-net,netdev=net0 \
        -display gtk
# 端到端集成测试:真机 user-manager 跑 tx 全链路;无用户会话时 exit 77 SKIP
# 参数透传: just integration-test [--tamper-step-args](本机 just 不透传位置参数,经 {{args}} 插值取参,与 dev-install 同法)
integration-test args='':
    bash tests/integration/tx_service_roundtrip.sh "{{args}}"
