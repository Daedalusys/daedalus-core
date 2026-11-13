// Package version 声明 core 自身的模块标识。
//
// ModulePath 只描述 core 这一个仓:SDK 与 plugins 各自的模块路径由各自 go.mod 决定,
// 不再借用本常量。此前的错位见 daedalus-sdk#43。
package version

// ModulePath 是 core 的模块导入路径,也是 core 内部包导入路径的固定前缀。
const ModulePath = "github.com/Daedalusys/daedalus-core"
