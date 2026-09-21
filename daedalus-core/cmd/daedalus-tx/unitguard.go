package main

// unitguard.go —— service.set 的用户域路径守卫与单元名/目录解析(todo 22)。
//
// 守卫是 UNCONDITIONAL 的(v1 无 tx systemd 单元, 用户域限制只靠这里, 计划
// todo 16/22 钉死): Propose 与 Apply/Rollback **各自独立**调用 guardUnitPath,
// 日志可被手改喂入 args/before_state —— 纵深防御, 每一次都重新解析重新守。
//
// 允许根(解析后的目标必须落在其一, 段边界前缀比较, 防 /home 匹配 /home2):
//   - $HOME/.config/systemd/user(v1 默认解析目录)
//   - /etc/systemd/user(系统侧用户单元只读落位)
//   - --unit-dir 旗标的字面根(集成测试逃生舱)
//
// 绝对禁区(先于允许根检查, 逐字错误): /etc/systemd/system/、/usr/lib/systemd/system/
// 之下任何路径 → "v1 supports user-scope units only: <path>"。
// 穿越/glob/注入字符在拼路径之前已被单元名正则整词拒绝。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// unitNamePattern 单元名字符集整词匹配: 字母数字下划线点 @ 连字符。
// 字符类天然排除 `/` `\` 空格 ; | $ ( ) 引号 空字节等 —— 路径分隔与 shell
// 注入面在源头即不存在(T6 同源手法)。
var unitNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)

// systemScopeRejectDirs v1 绝对禁区: 系统级单元目录(系统域属未来特权 helper 设计)。
var systemScopeRejectDirs = []string{"/etc/systemd/system", "/usr/lib/systemd/system"}

// unitDirCtxKey --unit-dir 的 context 载体键(commands.go 写入, 适配器读取;
// 不改 T15 的 Adapter 接口签名, 旗标只影响解析目录)。
type unitDirCtxKey struct{}

// withUnitDir 把非空覆写目录挂进 ctx; 空串(未给旗标)原样返回。
func withUnitDir(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, unitDirCtxKey{}, dir)
}

// unitDirFrom 读取 ctx 内的 --unit-dir 覆写; 未设置 → ""。
func unitDirFrom(ctx context.Context) string {
	s, _ := ctx.Value(unitDirCtxKey{}).(string)
	return s
}

// defaultUserUnitDir v1 默认解析目录 $HOME/.config/systemd/user。
func defaultUserUnitDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法解析 $HOME 以确定用户单元目录: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// normalizeUnitName 名称门(正则整词) + 隐式 .service 后缀(已带则不重复追加)。
func normalizeUnitName(name string) (string, error) {
	if name == "" || !unitNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid unit name: %q (仅允许单元名字符集)", name)
	}
	if strings.HasSuffix(name, ".service") {
		return name, nil
	}
	return name + ".service", nil
}

// resolveUnitFile 解析目录 = --unit-dir 覆写(转绝对+清洗)或 $HOME 默认;
// 返回 `<dir>/<unit>` 的清洗后绝对路径(尚未做符号链接解析, 那是守卫的事)。
func resolveUnitFile(ctx context.Context, unit string) (string, error) {
	dir := unitDirFrom(ctx)
	if dir == "" {
		var err error
		if dir, err = defaultUserUnitDir(); err != nil {
			return "", err
		}
	}
	abs, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return "", fmt.Errorf("解析单元目录失败: %w", err)
	}
	return filepath.Join(abs, unit), nil
}

// guardUnitPath UNCONDITIONAL 守卫:
//  1. 对目标做符号链接解析(目标不存在时解析至最近存在祖先再回接剩余段 ——
//     新建单元与目录级符号链接换址同样覆盖);
//  2. 解析结果落入系统级禁区目录 → 拒;
//  3. 解析结果必须位于允许根(同样先解析)之一内 → 否则拒。
//
// 一切拒绝共用逐字消息 "v1 supports user-scope units only: <path>":
// 越出用户域是同一个语义, 消息中的 <path> 为未解析的落盘路径(便于人工定位)。
func guardUnitPath(ctx context.Context, path string) error {
	resolved := resolveSymlinks(path)
	if isSystemScopePath(resolved) {
		return fmt.Errorf("v1 supports user-scope units only: %s", path)
	}
	for _, root := range permittedRoots(ctx) {
		if pathWithin(resolved, resolveSymlinks(root)) {
			return nil
		}
	}
	return fmt.Errorf("v1 supports user-scope units only: %s", path)
}

// isSystemScopePath 清洗后的绝对路径是否落在系统级单元目录(含目录本身)之下。
func isSystemScopePath(p string) bool {
	for _, d := range systemScopeRejectDirs {
		if pathWithin(p, d) {
			return true
		}
	}
	return false
}

// permittedRoots 允许根全集: HOME 用户根(不可解析则跳过, 守卫只会更严)、
// /etc/systemd/user、显式 --unit-dir 字面根。
func permittedRoots(ctx context.Context) []string {
	roots := make([]string, 0, 3)
	if def, err := defaultUserUnitDir(); err == nil {
		roots = append(roots, def)
	}
	roots = append(roots, "/etc/systemd/user")
	if ov := unitDirFrom(ctx); ov != "" {
		if abs, err := filepath.Abs(filepath.Clean(ov)); err == nil && abs != "/" && abs != "" {
			roots = append(roots, abs)
		}
	}
	return roots
}

// pathWithin 段边界前缀比较: p 等于 root 或位于 root/ 之下(防 /home vs /home2 误匹配)。
func pathWithin(p, root string) bool {
	if root == "" || root == "/" {
		return false
	}
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// resolveSymlinks 尽力解析符号链接: 目标存在 → 全量 EvalSymlinks; 不存在 →
// 上溯至最近存在的祖先解析后回接剩余段(新建单元文件的目录级换址同样被识破)。
// 连 "/" 都解析失败(理论不可能)时退回清洗原值 —— 保守拒绝方在守卫, 不在此。
func resolveSymlinks(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	parent := filepath.Dir(p)
	if parent == p {
		return filepath.Clean(p)
	}
	return filepath.Join(resolveSymlinks(parent), filepath.Base(p))
}
