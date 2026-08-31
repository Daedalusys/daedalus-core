//go:build demo

// paths_demo.go: 仅在 `-tags demo` 时编译,接管 paths.go 的同名路径变量。
// 从 $DAEDALUS_DEV_PATHS 指向的绝对路径文件,或从当前工作目录逐级向上
// 查找的 daedalus-dev.toml 读取 dev 路径配置(Prefix 必填,Deno 可选,
// 留空时回退到 PATH 上的 deno)。init() 读取失败立即 exit 2 拒绝启动:
// demo 构建绝不静默退化为 prod 行为(refuse-or-die)。
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

// devPathsConfig 对应 daedalus-dev.toml 的 schema:
// Prefix 是 dev 安装根(如 /home/user/.local);Deno 可选,缺省在 PATH 找 deno。
type devPathsConfig struct {
	Prefix string
	Deno   string `toml:"deno,omitempty"`
}

// devPathsFileName 是向上搜索的 dev 配置文件名。
const devPathsFileName = "daedalus-dev.toml"

// devPathsEnv 是显式指定 dev 配置文件绝对路径的环境变量(优先级最高)。
const devPathsEnv = "DAEDALUS_DEV_PATHS"

// denoBinary/devPrefix 先以 prod 镜像位作为缺省值(main_test.go 的
// prod 断言在 -tags demo 下也编译运行),init() 随后用 dev 配置覆盖;
// devMode 恒为 true,buildStartTokens 的镜像路径重写分支不会被折叠掉。
var (
	denoBinary = "/usr/local/bin/deno"
	devPrefix  = ""
)

const devMode = true

// loadDevPaths 解析 dev 路径配置:优先 $DAEDALUS_DEV_PATHS(须为绝对路径);
// 否则从 cwd 逐级向上查找 daedalus-dev.toml 直到根目录;都没有则报错。
func loadDevPaths() (devPathsConfig, error) {
	if env := os.Getenv(devPathsEnv); env != "" {
		if !filepath.IsAbs(env) {
			return devPathsConfig{}, fmt.Errorf("%s 必须是绝对路径,得到 %q", devPathsEnv, env)
		}
		return readDevPathsConfig(env)
	}
	dir, err := os.Getwd()
	if err != nil {
		return devPathsConfig{}, fmt.Errorf("无法获取工作目录: %w", err)
	}
	for {
		candidate := filepath.Join(dir, devPathsFileName)
		cfg, err := readDevPathsConfig(candidate)
		if err == nil {
			return cfg, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return devPathsConfig{}, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return devPathsConfig{}, fmt.Errorf("未找到 %s(可设 %s 指定绝对路径)", devPathsFileName, devPathsEnv)
		}
		dir = parent
	}
}

// readDevPathsConfig 用 BurntSushi/toml 解析 path 处的 dev 配置;
// 文件不存在、损坏或 Prefix 为空都返回 error(fail-closed)。
func readDevPathsConfig(path string) (devPathsConfig, error) {
	var cfg devPathsConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return devPathsConfig{}, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	if cfg.Prefix == "" {
		return devPathsConfig{}, fmt.Errorf("%s 缺少必填字段 Prefix", path)
	}
	return cfg, nil
}

// init 在 demo 构建启动时强制加载 dev 路径配置;失败 exit 2
// (与 main.go 的 exitUsage 同值),错误消息含 "demo build" 便于排查。
// go test 运行时跳过强校验,允许测试用例自行注入路径变量。
func init() {
	if testing.Testing() {
		return
	}
	cfg, err := loadDevPaths()
	if err != nil {
		fmt.Fprintf(os.Stderr, "daedalus-host: demo build requires dev paths config: %v\n", err)
		os.Exit(2)
	}
	devPrefix = cfg.Prefix
	denoBinary = cfg.Deno
	if denoBinary == "" {
		path, err := exec.LookPath("deno")
		if err != nil {
			fmt.Fprintf(os.Stderr, "daedalus-host: demo build requires deno on PATH (or set [dev_paths] deno in %s): %v\n", devPathsFileName, err)
			os.Exit(2)
		}
		denoBinary = path
	}
}
