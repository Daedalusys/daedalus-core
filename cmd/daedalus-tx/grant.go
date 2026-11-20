package main

// grant.go —— 条子(grant)的签发面: add / list / revoke。
//
// 权威身份是文件属主 uid(SUDO_USER 优先), 不是 JSON 里自报的 issued_by;
// 签发前用消费侧同一个 grant.Authorize 自检, 越界条子当场拒, 不等消费侧兜。
// 条子目录默认 /run/daedalus/grants(tmpfs, 重启即清), DAEDALUS_GRANTS_DIR 可覆盖。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Daedalusys/daedalus-sdk/audit"
	"github.com/Daedalusys/daedalus-sdk/autonomy"
	"github.com/Daedalusys/daedalus-sdk/grant"
)

const (
	// defaultGrantsDir 是条子目录的生产位置: tmpfs 意味着条子不跨重启存活。
	defaultGrantsDir = "/run/daedalus/grants"
	// envGrantsDir 覆盖条子目录(测试隔离用); 空值视为未设置。
	envGrantsDir = "DAEDALUS_GRANTS_DIR"
	// defaultGrantTTL 是未给 --ttl 时的有效期: 取短值, 签长条子必须显式写。
	defaultGrantTTL = 30 * time.Minute
)

// grantIDPattern 是签发命令自己生成的 id 形状: g-<UTC 日期>-<8 位随机 hex>。
var grantIDPattern = regexp.MustCompile(`^g-[0-9]{8}-[0-9a-f]{8}$`)

// grantAddDoc 是 grant add 的 stdout 文档。
type grantAddDoc struct {
	ID        string   `json:"id"`
	Path      string   `json:"path"`
	Scope     []string `json:"scope"`
	IssuedBy  string   `json:"issued_by"`
	IssuedAt  string   `json:"issued_at"`
	ExpiresAt string   `json:"expires_at"`
	MaxUses   int      `json:"max_uses"`
	Note      string   `json:"note,omitempty"`
}

// grantListEntry 是 grant list 里一张条子的只读视图(used 来自用量记账)。
type grantListEntry struct {
	ID        string   `json:"id"`
	Scope     []string `json:"scope"`
	IssuedBy  string   `json:"issued_by"`
	IssuedAt  string   `json:"issued_at"`
	ExpiresAt string   `json:"expires_at"`
	MaxUses   int      `json:"max_uses"`
	Used      int      `json:"used"`
	Expired   bool     `json:"expired"`
}

// grantInvalidEntry 是目录里过不了严格解析的文件: 列出来比整条命令失败更有用,
// 消费侧本来也会拒这些文件。
type grantInvalidEntry struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

// grantListDoc 是 grant list 的 stdout 文档。
type grantListDoc struct {
	Grants  []grantListEntry    `json:"grants"`
	Invalid []grantInvalidEntry `json:"invalid,omitempty"`
}

// grantRevokeDoc 是 grant revoke 的 stdout 文档。
type grantRevokeDoc struct {
	Revoked string `json:"revoked"`
}

// cmdGrant 分派 grant 子命令。
func cmdGrant(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return failUsage(stdout, stderr, "grant 需要子命令: add / list / revoke(见 --help)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return cmdGrantAdd(rest, stdout, stderr)
	case "list":
		if len(rest) != 0 {
			return failUsage(stdout, stderr, "grant list 不接受参数")
		}
		return cmdGrantList(stdout, stderr)
	case "revoke":
		return cmdGrantRevoke(rest, stdout, stderr)
	default:
		return failUsage(stdout, stderr, fmt.Sprintf("未知 grant 子命令: %s", sub))
	}
}

// cmdGrantAdd 签发一张条子: 解析旗标 → 定身份 → 载档位表 → 自检 → 落盘(chown) → 审计。
func cmdGrantAdd(args []string, stdout, stderr io.Writer) int {
	opts, ok := parseGrantAddArgs(args, stdout, stderr)
	if !ok {
		return exitUsage
	}
	iss, err := resolveIssuer()
	if err != nil {
		return failRuntime(stdout, stderr, err.Error())
	}
	cfg, err := autonomy.LoadConfigured()
	if err != nil {
		// root 恒可签(危险名单为空), 非 root 则无人被授权: 给出可读提示而不是崩溃。
		if !errors.Is(err, autonomy.ErrNotFound) || iss.uid != 0 {
			return failRuntime(stdout, stderr, fmt.Sprintf("加载档位表失败(没有档位表时除 root 外无人可签发, 请先配置 [[signer]]): %v", err))
		}
		cfg = nil
	}

	now := time.Now().UTC()
	dir := grantsDir()
	// 同一天内随机段仍可能撞: 撞了就重生成, 绝不用 rename 悄悄盖掉既有条子。
	var id string
	for attempt := 0; attempt < 3; attempt++ {
		var err error
		id, err = newGrantID(now)
		if err != nil {
			return failRuntime(stdout, stderr, err.Error())
		}
		if _, err := os.Lstat(filepath.Join(dir, id+".json")); errors.Is(err, os.ErrNotExist) {
			break
		}
	}
	g := &grant.Grant{
		ID:        id,
		Scope:     opts.scopes,
		IssuedBy:  iss.name,
		IssuedAt:  now,
		ExpiresAt: now.Add(opts.ttl),
		MaxUses:   opts.count,
		Note:      opts.note,
	}
	// 签发侧自检与消费侧同一函数: 范围/时长/次数/危险对象在签发当场就拒。
	if err := grant.Authorize(cfg, g, iss.uid, iss.name, now); err != nil {
		args := grantAuditArgs(g)
		args["error"] = err.Error()
		stampGrant("grant_issue", args, "denied")
		return failRuntime(stdout, stderr, err.Error())
	}

	path, err := writeGrantFile(dir, g, iss.uid)
	if err != nil {
		args := grantAuditArgs(g)
		args["error"] = err.Error()
		stampGrant("grant_issue", args, "error")
		return failRuntime(stdout, stderr, err.Error())
	}
	stampGrant("grant_issue", grantAuditArgs(g), "success")
	printJSON(stdout, grantAddDoc{
		ID:        g.ID,
		Path:      path,
		Scope:     g.Scope,
		IssuedBy:  g.IssuedBy,
		IssuedAt:  g.IssuedAt.Format(time.RFC3339),
		ExpiresAt: g.ExpiresAt.Format(time.RFC3339),
		MaxUses:   g.MaxUses,
		Note:      g.Note,
	})
	return exitOK
}

// cmdGrantList 列出条子及用量: 坏文件单列, 用量记账坏行直接失败(少计等于放宽上限)。
func cmdGrantList(stdout, stderr io.Writer) int {
	dir := grantsDir()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return failRuntime(stdout, stderr, fmt.Sprintf("读条子目录 %s 失败: %v", dir, err))
	}
	usagePath, err := grant.UsageLogPath()
	if err != nil {
		return failRuntime(stdout, stderr, err.Error())
	}
	used, err := grant.UsedCounts(usagePath)
	if err != nil {
		return failRuntime(stdout, stderr, fmt.Sprintf("读用量记账失败(坏行不得静默少计): %v", err))
	}

	doc := grantListDoc{Grants: []grantListEntry{}, Invalid: []grantInvalidEntry{}}
	now := time.Now().UTC()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			doc.Invalid = append(doc.Invalid, grantInvalidEntry{File: entry.Name(), Error: err.Error()})
			continue
		}
		g, err := grant.Parse(data)
		if err != nil {
			doc.Invalid = append(doc.Invalid, grantInvalidEntry{File: entry.Name(), Error: err.Error()})
			continue
		}
		doc.Grants = append(doc.Grants, grantListEntry{
			ID:        g.ID,
			Scope:     g.Scope,
			IssuedBy:  g.IssuedBy,
			IssuedAt:  g.IssuedAt.Format(time.RFC3339),
			ExpiresAt: g.ExpiresAt.Format(time.RFC3339),
			MaxUses:   g.MaxUses,
			Used:      used[g.ID],
			Expired:   g.Expired(now),
		})
	}
	sort.Slice(doc.Grants, func(i, j int) bool { return doc.Grants[i].ID < doc.Grants[j].ID })
	sort.Slice(doc.Invalid, func(i, j int) bool { return doc.Invalid[i].File < doc.Invalid[j].File })
	printJSON(stdout, doc)
	return exitOK
}

// cmdGrantRevoke 撤销一张条子: 属主门(非 root 只能删自己的) → 删文件 → 审计。
func cmdGrantRevoke(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return failUsage(stdout, stderr, "grant revoke 需要恰好一个 <id> 参数")
	}
	id := args[0]
	path, err := grantPath(grantsDir(), id)
	if err != nil {
		return failUsage(stdout, stderr, err.Error())
	}
	iss, err := resolveIssuer()
	if err != nil {
		return failRuntime(stdout, stderr, err.Error())
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return failRuntime(stdout, stderr, fmt.Sprintf("条子 %q 不存在(%s)", id, path))
		}
		return failRuntime(stdout, stderr, fmt.Sprintf("读取条子 %q 失败: %v", id, err))
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return failRuntime(stdout, stderr, fmt.Sprintf("无法读取条子 %q 的属主", id))
	}
	if os.Geteuid() != 0 {
		// 非 root 时只认真实 uid: SUDO_USER 是普通环境变量, 信它等于把身份判定
		// 交给调用方; 与 add 不同, 这里没有 chown 失败兜底。
		if iss.uid != uint32(os.Getuid()) {
			args := map[string]any{
				"grant_id":  id,
				"issued_by": iss.name,
				"error":     "非 root 时 SUDO_USER 与真实 uid 不一致",
			}
			stampGrant("grant_revoke", args, "denied")
			return failRuntime(stdout, stderr, fmt.Sprintf(
				"非 root 时不能以 %q(uid=%d)的身份撤销条子(SUDO_USER 可被任意设置); 需要 root 才能以他人身份操作",
				iss.name, iss.uid))
		}
		if st.Uid != iss.uid {
			args := map[string]any{
				"grant_id":  id,
				"issued_by": iss.name,
				"error":     "条子属主不是发起人",
			}
			stampGrant("grant_revoke", args, "denied")
			return failRuntime(stdout, stderr, fmt.Sprintf(
				"条子 %q 的属主 uid=%d 不是发起人 %q(uid=%d); 撤销别人的条子需要 root",
				id, st.Uid, iss.name, iss.uid))
		}
	}
	// 撤销坏条子也必须能成: 解析只用来补审计字段, 失败不阻止删除。
	auditArgs := map[string]any{"grant_id": id}
	if data, err := os.ReadFile(path); err == nil {
		if g, err := grant.Parse(data); err == nil {
			auditArgs = grantAuditArgs(g)
		}
	}
	if err := os.Remove(path); err != nil {
		auditArgs["error"] = err.Error()
		stampGrant("grant_revoke", auditArgs, "error")
		return failRuntime(stdout, stderr, fmt.Sprintf("删除条子 %q 失败: %v", id, err))
	}
	stampGrant("grant_revoke", auditArgs, "success")
	printJSON(stdout, grantRevokeDoc{Revoked: id})
	return exitOK
}

// grantAddOptions 是 grant add 的旗标集合。
type grantAddOptions struct {
	scopes []string
	ttl    time.Duration
	count  int
	note   string
}

// parseGrantAddArgs 解析 add 的旗标并复核全局硬上限; 任何形状错都是用法错误(exit 2)。
func parseGrantAddArgs(args []string, stdout, stderr io.Writer) (grantAddOptions, bool) {
	opts := grantAddOptions{ttl: defaultGrantTTL, count: 1}
	seen := make(map[string]bool, 3)
	for i := 0; i < len(args); i++ {
		name := args[i]
		switch name {
		case "--scope", "--ttl", "--count", "--note":
		default:
			failUsage(stdout, stderr, fmt.Sprintf("未知参数: %q", name))
			return opts, false
		}
		if i+1 >= len(args) || args[i+1] == "" {
			failUsage(stdout, stderr, name+" 需要跟随非空 <值> 参数")
			return opts, false
		}
		value := args[i+1]
		i++
		if name != "--scope" {
			if seen[name] {
				failUsage(stdout, stderr, name+" 至多出现一次")
				return opts, false
			}
			seen[name] = true
		}
		switch name {
		case "--scope":
			if _, err := autonomy.ParseScope(value); err != nil {
				failUsage(stdout, stderr, fmt.Sprintf("--scope %q 非法: %v", value, err))
				return opts, false
			}
			opts.scopes = append(opts.scopes, value)
		case "--ttl":
			d, err := time.ParseDuration(value)
			if err != nil {
				failUsage(stdout, stderr, fmt.Sprintf("--ttl %q 不是合法时长(如 30m、2h): %v", value, err))
				return opts, false
			}
			opts.ttl = d
		case "--count":
			n, err := strconv.Atoi(value)
			if err != nil {
				failUsage(stdout, stderr, fmt.Sprintf("--count %q 不是整数: %v", value, err))
				return opts, false
			}
			opts.count = n
		case "--note":
			opts.note = value
		}
	}
	if len(opts.scopes) == 0 {
		failUsage(stdout, stderr, "grant add 至少需要一个 --scope")
		return opts, false
	}
	if opts.ttl <= 0 {
		failUsage(stdout, stderr, fmt.Sprintf("--ttl 必须为正时长, 得到 %s", opts.ttl))
		return opts, false
	}
	// 硬上限常量复用 SDK 的单一事实源, 不在 CLI 再写一份 24h/10。
	if opts.ttl > grant.GlobalMaxTTL {
		failUsage(stdout, stderr, fmt.Sprintf("--ttl %s 超过全局上限 %s", opts.ttl, grant.GlobalMaxTTL))
		return opts, false
	}
	if opts.count <= 0 {
		failUsage(stdout, stderr, fmt.Sprintf("--count 必须为正整数, 得到 %d", opts.count))
		return opts, false
	}
	if opts.count > grant.GlobalMaxUses {
		failUsage(stdout, stderr, fmt.Sprintf("--count %d 超过全局上限 %d", opts.count, grant.GlobalMaxUses))
		return opts, false
	}
	return opts, true
}

// issuer 是条子的真实发起人: 名字进 issued_by, uid 决定文件属主与签发权限。
type issuer struct {
	name string
	uid  uint32
}

// resolveIssuer 解析真实发起人: SUDO_USER 优先(否则当前 uid)。
// SUDO_USER 查不到用户时拒绝而不是猜: 身份就是文件属主, 猜一个 uid 等于伪造授权。
func resolveIssuer() (issuer, error) {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		u, err := user.Lookup(sudoUser)
		if err != nil {
			return issuer{}, fmt.Errorf("SUDO_USER=%q 查不到对应用户, 拒绝签发: %v", sudoUser, err)
		}
		uid, err := parseUserUID(u)
		if err != nil {
			return issuer{}, err
		}
		return issuer{name: sudoUser, uid: uid}, nil
	}
	uid := uint32(os.Getuid())
	if u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10)); err == nil {
		return issuer{name: u.Username, uid: uid}, nil
	}
	// 查不到名字也照签: 名字只作线索, 权威是 uid。
	return issuer{name: fmt.Sprintf("uid-%d", uid), uid: uid}, nil
}

// parseUserUID 把 user.User 的数字 uid 解析成 uint32。
func parseUserUID(u *user.User) (uint32, error) {
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("用户 %q 的 uid %q 非法: %v", u.Username, u.Uid, err)
	}
	return uint32(uid), nil
}

// newGrantID 生成条子 id: g-<UTC 日期>-<8 位随机 hex>, 随机段用 crypto/rand。
func newGrantID(now time.Time) (string, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成条子 id 失败: %v", err)
	}
	return fmt.Sprintf("g-%s-%s", now.UTC().Format("20060102"), hex.EncodeToString(raw[:])), nil
}

// grantsDir 解析条子目录: DAEDALUS_GRANTS_DIR 覆盖, 否则生产位置。
func grantsDir() string {
	if v := os.Getenv(envGrantsDir); v != "" {
		return v
	}
	return defaultGrantsDir
}

// grantPath 拼出条子文件路径并做目录包含校验: id 来自命令行, 绝不许逃出条子目录。
func grantPath(dir, id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("条子 id 不能为空")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return "", fmt.Errorf("条子 id %q 非法(不得含路径分隔符或取值为 . / ..)", id)
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("条子 id %q 非法(不得含控制字符)", id)
		}
	}
	path := filepath.Join(dir, id+".json")
	if filepath.Dir(path) != filepath.Clean(dir) {
		return "", fmt.Errorf("条子 id %q 非法(解析后逃出条子目录)", id)
	}
	return path, nil
}

// writeGrantFile 原子写入条子文件并把属主改成真实发起人。
// 写入前先过消费侧同一个 Parse: 签发出去的文件必须能读回来。
func writeGrantFile(dir string, g *grant.Grant, ownerUID uint32) (string, error) {
	data, err := json.Marshal(g)
	if err != nil {
		return "", fmt.Errorf("序列化条子失败: %v", err)
	}
	if _, err := grant.Parse(data); err != nil {
		return "", fmt.Errorf("生成的条子未通过严格解析: %v", err)
	}
	// 生产目录由 tmpfiles 建好并 chgrp daedalus-signers; 这里只兜底不存在的情况。
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return "", fmt.Errorf("创建条子目录 %s 失败: %v", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".grant-*.tmp")
	if err != nil {
		return "", fmt.Errorf("在 %s 建临时文件失败: %v", dir, err)
	}
	tmpPath := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("写条子失败: %v", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("设置条子权限失败: %v", err)
	}
	// chown 失败即整体失败: 否则条子会以当前 euid(常是 root)的属主签出, 身份就成了假的。
	if err := os.Chown(tmpPath, int(ownerUID), -1); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("把条子属主改成 uid=%d 失败(身份=文件属主, 不能退化成签发进程的 uid): %v", ownerUID, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("flush 条子失败: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("关闭条子临时文件失败: %v", err)
	}
	final := filepath.Join(dir, g.ID+".json")
	if err := os.Rename(tmpPath, final); err != nil {
		return "", fmt.Errorf("落位条子 %s 失败: %v", final, err)
	}
	renamed = true
	return final, nil
}

// grantAuditArgs 是签发/撤销审计的 args 最小集(与设计约定的键一致)。
func grantAuditArgs(g *grant.Grant) map[string]any {
	return map[string]any{
		"grant_id":    g.ID,
		"scope":       g.Scope,
		"ttl_seconds": int64(g.ExpiresAt.Sub(g.IssuedAt) / time.Second),
		"max_uses":    g.MaxUses,
		"issued_by":   g.IssuedBy,
	}
}

// stampGrant 发一条条子审计(尽力而为: 写失败静默, 与事务审计同纪律)。
func stampGrant(tool string, args map[string]any, outcome string) {
	_, _ = audit.LogAudit(audit.Entry{ //nolint:errcheck // 尽力而为, 不拖垮签发本体
		Identity: txIdentity,
		Tool:     tool,
		Args:     argsVal(args),
		Outcome:  outcome,
	})
}
