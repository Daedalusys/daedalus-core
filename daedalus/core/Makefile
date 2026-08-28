# Daedalus Go 核心工作区构建脚本(计划 todo 1)
#
# 构建规范(全平台一致的静态产物):
#   - CGO_ENABLED=0    纯 Go 静态链接,不依赖 glibc,可直接放进 bootc 镜像任意阶段
#   - GOTOOLCHAIN=local 禁止构建期偷偷下载新版工具链(Metis M1 风险规避)
#   - -trimpath        抹除二进制中的本机源码路径,避免信息泄漏并利于可复现构建
#   - vendor/ 不入库  Go 仅 go.mod + go.sum 入库;`go build` 自动从 module proxy 下载到
#                      GOMODCACHE(首次构建需联网);GOTOOLCHAIN=local 仍禁止构建期自动下
#                      载新版工具链(与依赖下载是两条独立路径);升级依赖时需要
#                      `go get` + `go mod tidy`,本机直连 proxy.golang.org 超时时可临时
#                      `GOPROXY=https://goproxy.cn,direct`
#
# 用法:
#   make build   编译 ./cmd/... 全部命令 → bin/
#   make test    运行单元/集成测试(go test ./...)
#   make vet     静态检查(go vet ./...)
#   make fmt     格式化全部非 vendor 源码(gofmt -w)
#   make verify  校验依赖内容哈希(go mod verify)
#   make clean   清理 bin/ 构建产物

# 每个 cmd/* 子目录对应一个可交付二进制(当前仅 daedalus-smoke,后续任务扩展)
CMDS := $(notdir $(wildcard cmd/*))

.PHONY: build test vet fmt verify clean

build:
	CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -o bin/ ./cmd/...

test:
	CGO_ENABLED=0 GOTOOLCHAIN=local go test ./...

vet:
	CGO_ENABLED=0 GOTOOLCHAIN=local go vet ./...

# 只格式化本仓库源码,vendor/ 属于第三方快照,不得改动
fmt:
	find . -name '*.go' -not -path './vendor/*' -exec gofmt -w {} +

verify:
	go mod verify

clean:
	rm -rf bin
