# daedalus-core v0.2.0 Release Notes

## ⚠️ Breaking Change: 模块路径迁移

模块路径从 `github.com/daedalus-os/daedalus/core` 改为
`github.com/Daedalusys/daedalus-core`。

旧路径下的 v0.1.x 版本已通过 go.mod `retract [v0.1.0, v0.2.0)` 声明撤回,
`go get` 不会解析到这些版本。

### 迁移步骤

1. **go.mod**:依赖方 go.mod 的 require 行改为新路径(开发态可加 replace 指向本地 checkout):

   ```go
   require github.com/Daedalusys/daedalus-core v0.2.0
   replace github.com/Daedalusys/daedalus-core => ../daedalus-core
   ```

2. **import 全局 rename**:所有 `import "github.com/daedalus-os/daedalus/core/..."`
   改为 `import "github.com/Daedalusys/daedalus-core/..."`(sed 全局替换即可):

   ```bash
   grep -rl 'github.com/daedalus-os/daedalus/core' --include='*.go' . | xargs sed -i 's|github.com/daedalus-os/daedalus/core|github.com/Daedalusys/daedalus-core|g'
   ```

3. **验证**:`go mod tidy && go build ./... && go test ./...`。

## 其它变更

- **3 仓平级拆分**:daedalus-core(运行时 + 镜像构建)+ daedalus-sdk(11 个安全核心包)+
  daedalus-plugins(6 个 Go 能力插件 monorepo);本地开发以平级目录形态经 go.work 桥接。
- **跨仓 release 流水线**:daedalus-plugins 仓 release 出 6 个 `*.plugin.zip`
  (fs/shell/pkg/sysinfo/service/blueprint),core 镜像构建经
  `scripts/fetch-plugins.sh`(默认 `gh release download`,本地 `--local-zip-dir` 兜底)
  拉取解压进镜像树安装态。
- **插件打包/校验**:`daedalus-plugin-pack` 注入逐条目 sha256 checksums + manifest
  规范化自摘要,zip-slip 九道防线;解压即校验,fail-closed。