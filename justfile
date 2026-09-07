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

# Build Daedalus container image
# --network=host:让容器共享 host 网络栈,容器内 127.0.0.1 才指 host(用 host 的
#   127.0.0.1:7890 proxy);默认 podman 用 slirp4netns,127.0.0.1 指容器自己。
# --env HTTP_PROXY / --env HTTPS_PROXY:把 host 的 env 透传给容器,77-xrdp.sh
#   之类 dnf 调用能拿到(若 host 没设,容器里就是空,dnf 直连不变)。
# --cache-from:复用上一次的 localhost/daedalus-os:latest 镜像作 cache 源,
#   改 build.sh / Containerfile 之外的内容时,整个 RUN 步骤直接命中 cache,
#   dnf install + Go compile 全跳过,build 从 5-10 分钟降到 1-2 分钟。
# --jobs:BuildKit 内部独立步骤并行(对单步骤 RUN 帮助有限,加上零成本)。
# --network=host:让容器共享 host 网络栈,容器内 127.0.0.1 才指 host(用 host 的
#   127.0.0.1:7890 proxy);默认 podman 用 slirp4netns,127.0.0.1 指容器自己。
# --env HTTP_PROXY / --env HTTPS_PROXY:把 host 的 env 透传给容器,77-xrdp.sh
#   之类 dnf 调用能拿到(若 host 没设,容器里就是空,dnf 直连不变)。
# --cache-from 故意不传:本机 buildkit 版本对 --cache-from=localhost/...:tag 解析
#   有问题(报 "repository must contain neither a tag nor digest"),放弃跨 build
#   镜像层复用。Containerfile 已拆 8 stage + dnf cache mount,单次 build 内
#   已经够快;第二次 build 想更快就手动 `podman pull localhost/daedalus-os:latest`
#   后再跑,或后续升级 buildkit 再加回。
# 这几行**不影响 CI**(github-actions runner 上 HTTP_PROXY 不设,dnf 直连仓库不受影响)。
# 凭据透传:DAEDALUS_DEV_USER/PASS 从环境或 .env 取,空值时 78 脚本走公开构建模式(不注入账号)
build: sync
    podman build --jobs=$(nproc) --network=host --env HTTP_PROXY --env HTTPS_PROXY --env DAEDALUS_DEV_USER="{{ env_var_or_default('DAEDALUS_DEV_USER', '') }}" --env DAEDALUS_DEV_PASS="{{ env_var_or_default('DAEDALUS_DEV_PASS', '') }}" --platform=linux/amd64 --security-opt=label=disable --cap-add=all --device /dev/fuse --build-arg IMAGE_NAME=daedalus-os --build-arg IMAGE_REGISTRY=localhost --build-arg VARIANT=kde -t localhost/daedalus-os:latest -f Containerfile .

# --no-cache 版 build:改 build.sh / 脚本内容后必须用它。
# 原因:脚本经 --mount=type=bind,from=ctx 进 RUN,bind-mount 内容不进 Buildah
# 缓存 key → 改了脚本、甚至改了 build.sh 的去重 bug,stage RUN 仍 "Using cache"
# 复用旧层(镜像一直没 KDE 就是这个坑)。要真重跑改 build.sh 影响的 stage 就 --no-cache。
# 凭据透传:DAEDALUS_DEV_USER/PASS 从环境或 .env 取,空值时 78 脚本走公开构建模式(不注入账号)
build-nocache: sync
    podman build --no-cache --jobs=$(nproc) --network=host --env HTTP_PROXY --env HTTPS_PROXY --env DAEDALUS_DEV_USER="{{ env_var_or_default('DAEDALUS_DEV_USER', '') }}" --env DAEDALUS_DEV_PASS="{{ env_var_or_default('DAEDALUS_DEV_PASS', '') }}" --platform=linux/amd64 --security-opt=label=disable --cap-add=all --device /dev/fuse --build-arg IMAGE_NAME=daedalus-os --build-arg IMAGE_REGISTRY=localhost --build-arg VARIANT=kde -t localhost/daedalus-os:latest -f Containerfile .

# 镜像零残留断言(todo 15 接线,todo 16 收口;仅在 just build 成功后可跑):
# 断言镜像 /opt 内无 Python 源码/字节码、__pycache__、Deno 测试文件、Go 模块/依赖残留。
# 任一新布局构建产物泄漏进 rootfs 即 exit 1;干净时打印 OK。
verify-image:
    #!/usr/bin/env bash
    set -euo pipefail
    podman run --rm localhost/daedalus-os:latest sh -c 'find /opt \( -name "*.py" -o -name "*.pyc" -o -name "__pycache__" -o -name "*.test.ts" -o -name "go.mod" -o -name vendor \) | grep . && exit 1 || echo OK'

# 打包 5 个能力服务器(fs/shell/pkg/sysinfo/service)为 daedalus-plugin 并安装进镜像树
# (计划 todo 9;service 腿 = aios 计划 todo 11;构建镜像前执行)
# 同时安装带外 CLI 到 /usr/local/bin: host/audit/shell/service/tx(计划 todo 17 补 tx)
plugin-pack:
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
    # 流程(决策 22:构建期内建,无运行时安装;task 7 交接:安装态必经 Pack 注入 checksums):
    #   1) 构建全部 Go 静态二进制(含宿主 daedalus-host 与打包器 daedalus-plugin-pack);
    #   2) 同步二进制到插件源目录 daedalus/plugin/<cap>/bin/(源目录布局 = manifest + bin/);
    #   3) plugin-pack -in/-out 打 zip:Pack 注入逐条目 sha256 checksums + manifest 规范化自摘要;
    #   4) plugin-pack -verify --keep 把 zip 解压到镜像树安装态目录——解压即完整校验,
    #      任一摘要不符拒绝安装;安装态经 ./scripts/sync-daedalus.sh 同步为镜像 /opt/daedalus/plugins。
    root="$PWD"
    cd "$root/daedalus/core"
    CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -o bin/ ./cmd/...
    # 能力循环(aios 计划 todo 11 扩 service):源目录 daedalus/plugin/<cap>、
    # 安装态 plugins/daedalus.<cap>/,与 70 编号脚本"仅 chmod+提示"的分工互补——复制职责恒在本 recipe。
    for cap in fs shell pkg sysinfo service; do
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
    # aios 计划 todo 11:daedalus-service 按同款 task-21 形态双落位——
    #   插件安装态 plugins/daedalus.service/bin/(上方能力循环产出)供 systemd
    #   ExecStart(76 脚本 render-unit 指向);/usr/local/bin 副本服务镜像内以字面
    #   路径直接启动该二进制的 QA 链路(F3(e) service.query 手工管道)。两态互不替代。
    install -Dm0755 "bin/daedalus-service" "$root/daedalus/files/system/usr/local/bin/daedalus-service"
    # 计划 todo 17:daedalus-tx 为 out-of-band CLI(todo 16 裁决:无插件清单、无 systemd 单元、
    #   不进 76 脚本 render/handshake 环路)——与 audit/shell 同款 task-21 形态仅落
    #   /usr/local/bin,服务 copilot spawn 与用户直接 CLI(v1 执行模型 = 调用者进程)。
    install -Dm0755 "bin/daedalus-tx" "$root/daedalus/files/system/usr/local/bin/daedalus-tx"
    echo "plugin-pack: 5 个能力插件(fs/shell/pkg/sysinfo/service)已安装 -> daedalus/files/system/opt/daedalus/plugins/; host/audit/shell/service/tx 已安装 -> daedalus/files/system/usr/local/bin/"

# 开发态本地安装(计划 checkbox 1):把 dev 产物装进用户前缀,免镜像即可使用全套 CLI。
# 用法: just dev-install [前缀] (亦兼容 --prefix=X 形式);默认前缀 = $HOME/.local。
# 产物: <prefix>/bin/{daedalus-host,daedalus-audit,daedalus-shell}
#       <prefix>/share/daedalus/plugins/{5 个插件安装态} (消费 daedalus/core/bin/*.plugin.zip,不重新打包)
dev-install prefix='':
    #!/usr/bin/env bash
    set -euo pipefail
    # 与 go-build / go-test / plugin-pack 同款 fallback:SSH 远端非交互 shell
    # 不 source rc,asdf 用户的 go 不在 PATH,recipe 自带多源兜底。下面 line 133
    # 的 go build 不依赖前面 recipe 已跑过(独立流程)。
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
#
# 用 shebang 跑(而非一行 cd && go build ...):SSH 远端非交互 shell 不 source rc,
# asdf/mise 用户装的 go 不在 PATH。先 `command -v go` 试 PATH;找不到时按常见
# 位置 fallback(asdf shims / $HOME/.local/bin / 系统 /usr/local/go/bin),把
# 找到的 go 所在目录 prepend 到 PATH。这样:
#   - 本地交互 shell(go 已在 PATH):直接走 command -v,零成本
#   - GitHub Actions ubuntu-latest(apt 装 go 在 /usr/local/go/bin):走 fallback
#   - SSH 远端 asdf 用户:走 fallback 找到 ~/.asdf/shims/go
#   - SSH 远端裸系统 go:走 fallback 找到 /usr/local/go/bin/go
go-build:
    #!/usr/bin/env bash
    set -euo pipefail
    cd daedalus/core
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
go-test:
    #!/usr/bin/env bash
    set -euo pipefail
    cd daedalus/core
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
    cd daedalus/core
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

# Run test suite
# go test 那行同款 fallback(SSH 远端 asdf/mise 自找 go);deno + bash 走 PATH
# 已有,demo build 路径不影响。
test:
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
    cd daedalus/core && go test ./...
    deno test --allow-all tests/deno/
    # i18n 键集门禁(todo 30):en↔zh 对称 + t() 字面量双 locale 存在性
    bash tests/deno/i18n_keys.test.sh

# 打包 Copilot 为 daedalus-plugin(Deno runtime,计划 todo 8):
# 编译 pack/host → mktemp 暂存 5 个 .ts + 清单 → Pack 注入 checksums → 校验并解压安装态
# 产物: daedalus/core/bin/daedalus.copilot.plugin.zip(忽略)
#       daedalus/files/system/opt/daedalus/plugins/daedalus.copilot/(入库,sync 进镜像)
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
    # "image not known"(见 CI commit fe37ac4 同款处理)。
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
# 端到端集成测试(aios 计划 todo 23):真机 user-manager 跑 tx 全链路;无用户会话时 exit 77 SKIP
# 参数透传: just integration-test [--tamper-step-args](本机 just 不透传位置参数,经 {{args}} 插值取参,与 dev-install 同法)
integration-test args='':
    bash tests/integration/tx_service_roundtrip.sh "{{args}}"
