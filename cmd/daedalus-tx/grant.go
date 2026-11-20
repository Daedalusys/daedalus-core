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
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
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
	ID    string   `json:"id"`
	Scope []string `json:"scope"`
	// issued_by 是条子文件里的自报字段, 不可信, 只作线索; 身份以 owner_uid 为准。
	IssuedBy string `json:"issued_by"`
	// owner_uid 是文件属主, 唯一可信的身份来源; -1 表示取不到属主。
	OwnerUID  int    `json:"owner_uid"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
	MaxUses   int    `json:"max_uses"`
	Used      int    `json:"used"`
	Expired   bool   `json:"expired"`
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
		return failUsage(stdout, stderr, "grant 需要子命令: add / list / revoke(见 daedalus-tx --help)")
	}
	sub, rest := args[0], args[1:]
	if isHelpArg(sub) {
		fmt.Fprint(stdout, usage())
		return exitOK
	}
	switch sub {
	case "add":
		return cmdGrantAdd(rest, stdout, stderr)
	case "list":
		if len(rest) == 1 && isHelpArg(rest[0]) {
			fmt.Fprint(stdout, usage())
			return exitOK
		}
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

// isHelpArg 认 -h/--help/help: 子命令自己的帮助是成功路径, 不该被当成用法错。
func isHelpArg(a string) bool { return a == "-h" || a == "--help" || a == "help" }

// cmdGrantAdd 签发一张条子: 解析旗标 → 定身份 → 载档位表 → 自检 → 落盘(chown) → 审计。
func cmdGrantAdd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpArg(args[0]) {
		fmt.Fprint(stdout, usage())
		return exitOK
	}
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
		// 档位表「不存在」与「存在但不合法」是两种处境: 前者只是还没配置(除 root 外
		// 无人可签), 后者是配置坏了, 混成一句会让人照着错误的方向去修。
		if !errors.Is(err, autonomy.ErrNotFound) {
			return failRuntime(stdout, stderr, fmt.Sprintf("加载档位表失败(档位表存在但不可用, 请检查格式与 [[signer]] 条目): %v", err))
		}
		if iss.uid != 0 {
			return failRuntime(stdout, stderr, fmt.Sprintf("还没有档位表, 除 root 外无人可签发(请先配置 [[signer]]): %v", err))
		}
		cfg = nil
	}

	dir, err := grantsDir()
	if err != nil {
		return failRuntime(stdout, stderr, err.Error())
	}
	now := time.Now().UTC()
	// 同一天内随机段仍可能撞: 撞了就重生成, 绝不用 rename 悄悄盖掉既有条子。
	var id string
	for attempt := 0; attempt < 3; attempt++ {
		candidate, err := newGrantID(now)
		if err != nil {
			return failRuntime(stdout, stderr, err.Error())
		}
		id = candidate
		_, err = os.Lstat(filepath.Join(dir, id+".json"))
		if err == nil {
			continue
		}
		// 只有「不存在」才放行; 权限之类的错误不能被当成撞车无限重试。
		if !errors.Is(err, fs.ErrNotExist) {
			return failRuntime(stdout, stderr, fmt.Sprintf("检查条子 id %q 是否已存在失败: %v", id, err))
		}
		break
	}
	// 三次都撞车时循环不会给出可用 id; 复核一次, 绝不让后面的 rename 覆盖既有条子。
	if _, err := os.Lstat(filepath.Join(dir, id+".json")); err == nil {
		return failRuntime(stdout, stderr, fmt.Sprintf("生成唯一 id 失败: %q 连续三次撞车, 请重试", id))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return failRuntime(stdout, stderr, fmt.Sprintf("检查条子 id %q 是否已存在失败: %v", id, err))
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
	dir, err := grantsDir()
	if err != nil {
		return failRuntime(stdout, stderr, err.Error())
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return failRuntime(stdout, stderr, fmt.Sprintf("读条子目录 %s 失败: %v", dir, err))
	}
	// 只读命令经 grant.UsageLogPath → dirs.TxRoot 探测目录, 可能建出
	// $HOME/.local/share/daedalus/tx。这点副作用换的是「已用次数不许撒谎」:
	// 要做到零写需 SDK 提供「只解析不探测」的入口, 不值得为它放宽用量判定。
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
		// revoke 按 <id>.json 找文件, 所以列出来的 id 必须等于文件名; 手写文件里另写
		// 一个 id 只会造出「列得出来却撤不掉」的假条目, 归入 invalid[] 而不是放行。
		if entry.Name() != g.ID+".json" {
			doc.Invalid = append(doc.Invalid, grantInvalidEntry{
				File:  entry.Name(),
				Error: fmt.Sprintf("文件名与条子 id %q 不一致(id 以文件名为准才能撤销)", g.ID),
			})
			continue
		}
		// 取不到属主时用 -1 而不是 0: 0 是 root 的合法 uid, 不能拿来冒充「未知」。
		ownerUID := -1
		if fi, err := entry.Info(); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				ownerUID = int(st.Uid)
			}
		}
		doc.Grants = append(doc.Grants, grantListEntry{
			ID:        g.ID,
			Scope:     g.Scope,
			IssuedBy:  g.IssuedBy,
			OwnerUID:  ownerUID,
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
// 属主门只约束 CLI 路径: 文件系统层的删除权由条子目录的权限(sticky bit)决定。
func cmdGrantRevoke(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpArg(args[0]) {
		fmt.Fprint(stdout, usage())
		return exitOK
	}
	if len(args) != 1 {
		return failUsage(stdout, stderr, "grant revoke 需要恰好一个 <id> 参数")
	}
	id := args[0]
	dir, err := grantsDir()
	if err != nil {
		return failRuntime(stdout, stderr, err.Error())
	}
	path, err := grantPath(dir, id)
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
				"owner_uid": int(st.Uid),
				"issued_by": iss.name,
				"error":     "非 root 时 SUDO_USER 与真实 uid 不一致",
			}
			stampGrant("grant_revoke", args, "denied")
			return failRuntime(stdout, stderr, fmt.Sprintf(
				"非 root 时不能以 %q(uid=%d)的身份撤销条子(SUDO_USER 可被任意设置); 需要 root 才能以他人身份操作",
				iss.name, iss.uid))
		}
		if err := canRevoke(int(st.Uid), int(iss.uid), os.Geteuid()); err != nil {
			args := map[string]any{
				"grant_id":  id,
				"owner_uid": int(st.Uid),
				"issued_by": iss.name,
				"error":     err.Error(),
			}
			stampGrant("grant_revoke", args, "denied")
			return failRuntime(stdout, stderr, fmt.Sprintf("无法撤销条子 %q(发起人 %q uid=%d): %v", id, iss.name, iss.uid, err))
		}
	}
	// 撤销坏条子也必须能成: 解析只用来补审计字段, 失败不阻止删除。
	auditArgs := map[string]any{"grant_id": id}
	if data, err := os.ReadFile(path); err == nil {
		if g, err := grant.Parse(data); err == nil {
			auditArgs = grantAuditArgs(g)
		}
	}
	// 身份以文件属主为准: JSON 里的 issued_by 是自报字段, 只作线索;
	// grant_id 用命令行给的 id, 文件里自报的 id 可能与文件名不一致。
	auditArgs["grant_id"] = id
	auditArgs["owner_uid"] = int(st.Uid)
	if err := os.Remove(path); err != nil {
		auditArgs["error"] = err.Error()
		stampGrant("grant_revoke", auditArgs, "error")
		return failRuntime(stdout, stderr, fmt.Sprintf("删除条子 %q 失败: %v", id, err))
	}
	stampGrant("grant_revoke", auditArgs, "success")
	printJSON(stdout, grantRevokeDoc{Revoked: id})
	return exitOK
}

// canRevoke 判断调用者能否撤销这条条子。抽成纯函数是为了让「属主门」能被直接单测:
// 靠造「别人拥有的文件」来测在非 root 下根本做不到, 那道门就会长期无人看守。
func canRevoke(fileUID, issuerUID, euid int) error {
	if euid == 0 {
		return nil
	}
	if fileUID != issuerUID {
		return fmt.Errorf("条子属主 uid=%d 不是发起人 uid=%d; 撤销别人的条子需要 root", fileUID, issuerUID)
	}
	return nil
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
		if i+1 >= len(args) {
			failUsage(stdout, stderr, name+" 需要跟随 <值> 参数")
			return opts, false
		}
		value := args[i+1]
		// 以 -- 开头的是下一个旗标而不是值: 把它当值吞掉会让拼错的命令以怪方式通过。
		// --note 例外地允许空串 —— 空备注是合法输入。
		if strings.HasPrefix(value, "--") || (value == "" && name != "--note") {
			failUsage(stdout, stderr, name+" 需要跟随 <值> 参数")
			return opts, false
		}
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

// issuerIdentity 解析真实发起人: SUDO_USER 优先(它是 sudo 记录的原始调用者),
// 解析不出用户则 fail-closed 拒绝 —— 宁可不签, 也不把条子签成 root。
// 名字取 NSS 规范化后的 u.Username: 别名/大小写差异不该让名单里的用户被误判无权。
func issuerIdentity(euid int, sudoUser string, lookup func(string) (*user.User, error)) (int, string, error) {
	if sudoUser != "" {
		u, err := lookup(sudoUser)
		if err != nil {
			return 0, "", fmt.Errorf("SUDO_USER=%q 查不到对应用户, 拒绝签发: %v", sudoUser, err)
		}
		uid, err := parseUserUID(u)
		if err != nil {
			return 0, "", err
		}
		return int(uid), u.Username, nil
	}
	if u, err := lookup(strconv.Itoa(euid)); err == nil {
		return euid, u.Username, nil
	}
	// 查不到名字也照签: 名字只作线索, 权威是 uid。
	return euid, fmt.Sprintf("uid-%d", euid), nil
}

// lookupNameOrUID 让 issuerIdentity 的单一 lookup 缝同时覆盖两种解析:
// SUDO_USER 是名字, 而回退路径手里只有 euid 这个数字 id。
func lookupNameOrUID(key string) (*user.User, error) {
	if u, err := user.Lookup(key); err == nil {
		return u, nil
	}
	return user.LookupId(key)
}

// resolveIssuer 把当前进程的 euid 与 SUDO_USER 交给 issuerIdentity。
func resolveIssuer() (issuer, error) {
	uid, name, err := issuerIdentity(os.Geteuid(), os.Getenv("SUDO_USER"), lookupNameOrUID)
	if err != nil {
		return issuer{}, err
	}
	return issuer{name: name, uid: uint32(uid)}, nil
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
// 做成变量是为了让「连续撞车」这条罕见分支能在测试里被直接走到。
var newGrantID = func(now time.Time) (string, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成条子 id 失败: %v", err)
	}
	return fmt.Sprintf("g-%s-%s", now.UTC().Format("20060102"), hex.EncodeToString(raw[:])), nil
}

// grantsDir 解析条子目录: DAEDALUS_GRANTS_DIR 覆盖, 否则生产位置。
// 覆盖值必须是绝对路径 —— 相对路径会把条子写到调用者的工作目录, 与运行时目录约定不符。
func grantsDir() (string, error) {
	v := os.Getenv(envGrantsDir)
	if v == "" {
		return defaultGrantsDir, nil
	}
	if !filepath.IsAbs(v) {
		return "", fmt.Errorf("%s=%q 非法: 必须是绝对路径(否则条子会落到当前工作目录)", envGrantsDir, v)
	}
	return v, nil
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

// chownFile 是「把条子属主改成真实发起人」的唯一落点。抽成包级变量是为了让测试能
// 逐字断言 (path, uid): 非 root 下 chown 到自己本来就是 no-op, 否则这条承重门只测得到拒绝方向。
var chownFile = os.Chown

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
	if err := chownFile(tmpPath, int(ownerUID), -1); err != nil {
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
