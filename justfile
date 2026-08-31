# Default recipe: list available recipes
default:
    @just --list

# Sync Daedalus-owned files into base_image tree
sync:
    ./sync-daedalus.sh

# Build Daedalus container image
build: sync
    podman build --platform=linux/amd64 --security-opt=label=disable --cap-add=all --device /dev/fuse --build-arg IMAGE_NAME=daedalus-os --build-arg IMAGE_REGISTRY=localhost --build-arg VARIANT=kde -t localhost/daedalus-os:latest -f Containerfile .

# 镜像零残留断言(todo 15 接线,todo 16 收口;仅在 just build 成功后可跑):
# 断言镜像 /opt 内无 Python 源码/字节码、__pycache__、Deno 测试文件、Go 模块/依赖残留。
# 任一新布局构建产物泄漏进 rootfs 即 exit 1;干净时打印 OK。
verify-image:
    #!/usr/bin/env bash
    set -euo pipefail
    podman run --rm localhost/daedalus-os:latest sh -c 'find /opt \( -name "*.py" -o -name "*.pyc" -o -name "__pycache__" -o -name "*.test.ts" -o -name "go.mod" -o -name vendor \) | grep . && exit 1 || echo OK'

# 打包 4 个能力服务器为 daedalus-plugin 并安装进镜像树(计划 todo 9;构建镜像前执行)
plugin-pack:
    #!/usr/bin/env bash
    set -euo pipefail
    # 流程(决策 22:构建期内建,无运行时安装;task 7 交接:安装态必经 Pack 注入 checksums):
    #   1) 构建全部 Go 静态二进制(含宿主 daedalus-host 与打包器 daedalus-plugin-pack);
    #   2) 同步二进制到插件源目录 daedalus/plugin/<cap>/bin/(源目录布局 = manifest + bin/);
    #   3) plugin-pack -in/-out 打 zip:Pack 注入逐条目 sha256 checksums + manifest 规范化自摘要;
    #   4) plugin-pack -verify --keep 把 zip 解压到镜像树安装态目录——解压即完整校验,
    #      任一摘要不符拒绝安装;安装态经 ./sync-daedalus.sh 同步为镜像 /opt/daedalus/plugins。
    root="$PWD"
    cd "$root/daedalus/core"
    CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -o bin/ ./cmd/...
    for cap in fs shell pkg sysinfo; do
        id="daedalus.${cap}"
        src="$root/daedalus/plugin/${cap}"
        dest="$root/daedalus/files/system/opt/daedalus/plugins/${id}"
        mkdir -p "${src}/bin"
        cp -f "bin/daedalus-${cap}" "${src}/bin/daedalus-${cap}"
        chmod 0755 "${src}/bin/daedalus-${cap}"
        "./bin/daedalus-plugin-pack" -in "${src}" -out "bin/${id}.plugin.zip"
        mkdir -p "${dest}"
        # 解压器要求空目录(O_EXCL 不覆盖既有文件);本机权限面禁 rm,用 find -delete 清空。
        find "${dest}" -mindepth 1 -delete
        "./bin/daedalus-plugin-pack" -verify "bin/${id}.plugin.zip" --keep "${dest}"
    done
    # 宿主自身进镜像树 /usr/local/bin:构建期 76-daedalus-plugin-gen.sh 与 copilot wrapper(任务 8)都依赖它。
    install -Dm0755 "bin/daedalus-host" "$root/daedalus/files/system/usr/local/bin/daedalus-host"
    # copilot 运行期依赖的两个二进制同入 /usr/local/bin(任务 21: 修复镜像内审计/执行断链缺陷):
    #   audit.ts 生产默认路径 = /usr/local/bin/daedalus-audit(exec.ts 同理 = daedalus-shell),
    #   wrapper 的 deno --allow-run 旗标已放行这两个路径(沙箱旗标与解析顺序均不动)。
    #   注意: 插件安装态 plugins/daedalus.shell/bin/ 内的副本保持不动——systemd 单元仍经
    #   76 脚本 render-unit 指向插件内二进制; 此处副本仅服务 copilot 进程内 spawn 的字面路径。
    install -Dm0755 "bin/daedalus-audit" "$root/daedalus/files/system/usr/local/bin/daedalus-audit"
    install -Dm0755 "bin/daedalus-shell" "$root/daedalus/files/system/usr/local/bin/daedalus-shell"
    echo "plugin-pack: 4 个能力插件已安装 -> daedalus/files/system/opt/daedalus/plugins/; host/audit/shell 已安装 -> daedalus/files/system/usr/local/bin/"

# 开发态本地安装(计划 checkbox 1):把 dev 产物装进用户前缀,免镜像即可使用全套 CLI。
# 用法: just dev-install [前缀] (亦兼容 --prefix=X 形式);默认前缀 = $HOME/.local。
# 产物: <prefix>/bin/{daedalus-host,daedalus-audit,daedalus-shell}
#       <prefix>/share/daedalus/plugins/{5 个插件安装态} (消费 daedalus/core/bin/*.plugin.zip,不重新打包)
dev-install prefix='':
    #!/usr/bin/env bash
    set -euo pipefail
    # 本机 just(1.58)不把位置参数透传为 $1,经 {{prefix}} 插值取参;兼容 --prefix=X 旗标形式
    prefix="{{prefix}}"
    case "${prefix}" in
        --prefix=*) prefix="${prefix#--prefix=}" ;;
    esac
    # 默认前缀 = $HOME/.local
    if [ -z "${prefix}" ]; then
        prefix="${HOME}/.local"
    fi
    root="$PWD"
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
    # 建目录:<prefix>/bin 与 <prefix>/share/daedalus
    ${sudo} install -d "${prefix}/bin" "${prefix}/share/daedalus"
    # 复用 plan-1 构建(与 go-build/plugin-pack 逐字同旗标;不发明新构建形态)
    cd "${root}/daedalus/core"
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
    # 收尾:打印可复制的导出行与冒烟命令
    echo ""
    echo "dev-install 完成: 前缀=${prefix}"
    echo "请复制以下环境变量与命令:"
    echo "  export DAEDALUS_PLUGIN_DIR=${plugins_root}"
    echo "  export PATH=${prefix}/bin:\$PATH"
    echo "  ${prefix}/bin/daedalus-host -dir ${plugins_root} list"

# 列出开发态已安装插件(计划 checkbox 1 配套):优先用 <prefix>/bin 的 host,
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

# 构建全部 Go 静态二进制到 daedalus/core/bin/(计划 todo 15;对齐 core/Makefile 的 build 语义:
# CGO_ENABLED=0 纯静态、GOTOOLCHAIN=local 禁用工具链自动下载、-trimpath 可复现路径)
go-build:
    cd daedalus/core && CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -o bin/ ./cmd/...

# Go 全量单元测试(纯模块级,不依赖镜像;just test 的 Go 腿即此命令)
go-test:
    cd daedalus/core && go test ./...

# 显式下载 Go 模块依赖到 GOMODCACHE(vendor/ 不入库,首次构建需联网;`go build` 也会
# 隐式触发,此 recipe 仅用于预热缓存或排查模块网络问题)
deps:
    cd daedalus/core && go mod download

# Run test suite
test:
    cd daedalus/core && go test ./...
    deno test --allow-all tests/deno/

# 打包 Copilot 为 daedalus-plugin(Deno runtime,计划 todo 8):
# 编译 pack/host → mktemp 暂存 5 个 .ts + 清单 → Pack 注入 checksums → 校验并解压安装态
# 产物: daedalus/core/bin/daedalus.copilot.plugin.zip(忽略)
#       daedalus/files/system/opt/daedalus/plugins/daedalus.copilot/(入库,sync 进镜像)
copilot-plugin:
    ./pack-copilot-plugin.sh

# Build bootable ISO
iso:
    mkdir -p output
    podman run --rm -v "$(pwd)/output":/output --security-opt label=disable quay.io/centos-bootc/bootc-image-builder:latest --type iso --image-name localhost/daedalus-os:latest

# Build qcow2 and run in QEMU
qemu:
    mkdir -p output
    podman run --rm -v "$(pwd)/output":/output --security-opt label=disable quay.io/centos-bootc/bootc-image-builder:latest --type qcow2 --image-name localhost/daedalus-os:latest
    qemu-system-x86_64 -m 4096 -cdrom output/boot.qcow2 -enable-kvm -vga virtio
