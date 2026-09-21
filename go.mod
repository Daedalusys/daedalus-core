module github.com/Daedalusys/daedalus-core

go 1.25.0

retract (
	// v0.1.x 是模块路径迁移前的历史版本(旧路径 github.com/daedalus-os/daedalus/core);
	// 迁移到 github.com/Daedalusys/daedalus-core 后语义不兼容,retract 阻止误用。
	// 注:任务写的半开区间 [v0.1.0, v0.2.0) 在 go.mod 无法表达(仅支持闭区间),
	// v0.2.0 是当前发布版本不可 retract,故用 [v0.1.0, v0.1.9] 覆盖全部 v0.1.x。
	[v0.1.0, v0.1.9]
)

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/Daedalusys/daedalus-sdk v0.0.0-00010101000000-000000000000
	github.com/modelcontextprotocol/go-sdk v1.7.0
)

replace github.com/Daedalusys/daedalus-sdk => ../daedalus-sdk

require (
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)
