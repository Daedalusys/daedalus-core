//go:build !demo

// paths.go: prod 构建(默认,无 build tag)的路径常量。
// 所有路径在编译期写死为镜像内安装位;demo 构建(-tags demo)
// 由 paths_demo.go 接管同名变量,从本地 dev 配置解析路径。
package main

// denoBinary 是镜像内 Deno 运行时的固定路径(65-ai-safety.sh 安装位)。
var denoBinary = "/usr/local/bin/deno"

// devPrefix 是 demo 模式的 dev 根前缀;prod 恒为空串(路径不做改写)。
var devPrefix = ""

// devMode 是编译期常量:prod 恒为 false,buildStartTokens 里的重写分支
// 被编译器常量折叠整体消除,prod 行为与历史版本完全一致。
const devMode = false
