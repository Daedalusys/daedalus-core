package main

// pending.go —— 第三档的人工闸门: 待办(pending)+ 批准(approve)。
//
// 待办与批准的线上契约住 SDK 的 pending 包(core 与 plugins 共用一个事实源):
// 严格解析、id 形状门、argv 指纹都在那里, 本文件只管 core 侧怎么用。
// 批准目录 0755 root:root 是这道门的全部机制, 代码只做 fail-closed 的二次确认。
//
// 批准绑定的是具体 argv 而不是一类操作: 人看过的那份指纹与当前待办的指纹不一致
// 就拒绝批准, 因为待办文件的属主能在审查之后把整份内容改写。
//
// 请求者的权威是待办文件的 stat 属主: 待办 JSON 里的字段都是自报, 谁都能写。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Daedalusys/daedalus-sdk/pending"
)

const (
	// defaultPendingDir 是待办的生产位置: tmpfs 意味着待办不跨重启存活。
	defaultPendingDir = "/run/daedalus/pending"
	// defaultApprovalsDir 是批准记录的生产位置: 0755 root:root, 非 root 写不进去。
	defaultApprovalsDir = "/run/daedalus/approvals"
	// envPendingDir / envApprovalsDir 覆盖目录(测试隔离用); 空值视为未设置。
	envPendingDir    = "DAEDALUS_PENDING_DIR"
	envApprovalsDir  = "DAEDALUS_APPROVALS_DIR"
	approverIdentity = "root"
	// maxPendingFileBytes 给待办文件设上限: 待办目录属主可写, root 审查时不该被
	// 一份超大 JSON 拖成内存压力点; 超限的待办一律拒绝而不是截断后猜。
	maxPendingFileBytes = 64 << 10
)

// pendingListEntry 是待办的人类可读视图(附过期判定与 argv 指纹)。
type pendingListEntry struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	DesiredState string   `json:"desired_state"`
	Argv         []string `json:"argv"`
	ArgvSHA256   string   `json:"argv_sha256"`
	Scope        string   `json:"scope"`
	Reason       string   `json:"reason,omitempty"`
	CreatedAt    string   `json:"created_at"`
	ExpiresAt    string   `json:"expires_at"`
	Expired      bool     `json:"expired"`
}

// pendingInvalidEntry 是过不了严格解析的待办文件: 列出来比整条命令失败更有用。
type pendingInvalidEntry struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

// pendingListDoc 是 pending list 的 stdout 文档。
type pendingListDoc struct {
	Pending []pendingListEntry    `json:"pending"`
	Invalid []pendingInvalidEntry `json:"invalid"`
}

// approveDoc 是 approve 的 stdout 文档。argv 是**刚刚被批准的那条 argv 原文**:
// 待办文件属主可随时改写, 人必须从批准输出里看到自己到底批了什么。
type approveDoc struct {
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	Argv       []string `json:"argv"`
	ArgvSHA256 string   `json:"argv_sha256"`
	ApprovedBy string   `json:"approved_by"`
	ApprovedAt string   `json:"approved_at"`
}

// cmdPending 分派 pending 子命令。
func cmdPending(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return failUsage(stdout, stderr, "pending 需要子命令: list / show(见 --help)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		if len(rest) != 0 {
			return failUsage(stdout, stderr, "pending list 不接受参数")
		}
		return cmdPendingList(stdout, stderr)
	case "show":
		return cmdPendingShow(rest, stdout, stderr)
	default:
		return failUsage(stdout, stderr, fmt.Sprintf("未知 pending 子命令: %s", sub))
	}
}

// cmdPendingList 列出全部待办; 坏文件单列, 不让一条坏文件挡住人看其他待办。
func cmdPendingList(stdout, stderr io.Writer) int {
	dir := pendingDir()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return failRuntime(stdout, stderr, fmt.Sprintf("读待办目录 %s 失败: %v", dir, err))
	}
	doc := pendingListDoc{Pending: []pendingListEntry{}, Invalid: []pendingInvalidEntry{}}
	now := time.Now().UTC()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		pend, _, err := readPending(dir, id)
		if err != nil {
			doc.Invalid = append(doc.Invalid, pendingInvalidEntry{File: entry.Name(), Error: err.Error()})
			continue
		}
		doc.Pending = append(doc.Pending, pendingEntry(pend, now))
	}
	sort.Slice(doc.Pending, func(i, j int) bool { return doc.Pending[i].ID < doc.Pending[j].ID })
	sort.Slice(doc.Invalid, func(i, j int) bool { return doc.Invalid[i].File < doc.Invalid[j].File })
	printJSON(stdout, doc)
	return exitOK
}

// cmdPendingShow 显示某条待办的 argv 与待批准的指纹。
func cmdPendingShow(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return failUsage(stdout, stderr, "pending show 需要恰好一个 <id> 参数")
	}
	id := args[0]
	if err := pending.ValidateID(id); err != nil {
		return failUsage(stdout, stderr, err.Error())
	}
	pend, _, err := readPending(pendingDir(), id)
	if err != nil {
		return failRuntime(stdout, stderr, err.Error())
	}
	printJSON(stdout, pendingEntry(pend, time.Now().UTC()))
	return exitOK
}

// approveOptions 是 approve 的旗标集合。
type approveOptions struct {
	id        string
	expectSHA string
}

// parseApproveArgs 解析 approve 的 <pending-id> 与 --expect-sha256; 任何形状错都是用法错误。
// --expect-sha256 必填: 没有"人看过的指纹"这个锚, 批准就只能绑定 approve 那一刻的文件。
func parseApproveArgs(args []string, stdout, stderr io.Writer) (approveOptions, bool) {
	var opts approveOptions
	var positional []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "--expect-sha256":
			if i+1 >= len(args) || args[i+1] == "" {
				failUsage(stdout, stderr, "--expect-sha256 需要跟随非空 <hash> 参数")
				return opts, false
			}
			if opts.expectSHA != "" {
				failUsage(stdout, stderr, "--expect-sha256 至多出现一次")
				return opts, false
			}
			opts.expectSHA = args[i+1]
			i++
		default:
			if strings.HasPrefix(arg, "--") {
				failUsage(stdout, stderr, fmt.Sprintf("未知参数: %q", arg))
				return opts, false
			}
			positional = append(positional, arg)
		}
	}
	if len(positional) != 1 {
		failUsage(stdout, stderr, "approve 需要恰好一个 <pending-id> 参数")
		return opts, false
	}
	opts.id = positional[0]
	if err := pending.ValidateID(opts.id); err != nil {
		failUsage(stdout, stderr, err.Error())
		return opts, false
	}
	if opts.expectSHA == "" {
		failUsage(stdout, stderr,
			"approve 必须带 --expect-sha256 <hash>: 从 pending show/list 输出的 argv_sha256 抄过来, 批准才会绑定你看过的那一份 argv")
		return opts, false
	}
	return opts, true
}

// cmdApprove 由 root 批准一条待办: 校验存在/未过期/指纹一致 → 在批准目录写不可转让的记录。
func cmdApprove(args []string, stdout, stderr io.Writer) int {
	opts, ok := parseApproveArgs(args, stdout, stderr)
	if !ok {
		return exitUsage
	}
	id := opts.id
	// 机制上是批准目录的权限挡住非 root; 这里只做一次显式确认, 让拒绝理由可读。
	if geteuid() != 0 {
		auditApproveDenied(id, opts.expectSHA, "非 root 不得批准(批准目录 0755 root:root)")
		return failUsage(stdout, stderr, fmt.Sprintf(
			"批准待办 %q 需要 root(sudo daedalus-tx approve %s --expect-sha256 %s); /run/daedalus/approvals 只有 root 能写",
			id, id, opts.expectSHA))
	}

	pend, ownerUID, err := readPending(pendingDir(), id)
	if err != nil {
		auditApproveDenied(id, opts.expectSHA, err.Error())
		return failRuntime(stdout, stderr, err.Error())
	}
	now := time.Now().UTC()
	// 到点即作废(含恰好在 expires_at): 已过期的待办不能靠一次迟到的批准复活。
	if !now.Before(pend.ExpiresAt) {
		msg := fmt.Sprintf("待办 %q 已过期(expires_at=%s), 需要重新申请",
			id, pend.ExpiresAt.UTC().Format(time.RFC3339))
		auditApproveDenied(id, opts.expectSHA, msg)
		return failRuntime(stdout, stderr, msg)
	}

	// 批准必须绑定人看过的那一份 argv: 待办文件属主可在审查后整份改写, 指纹变了
	// 就说明"你以为批的 A"已不是"现在要执行的 B" —— 拒绝, 且绝不写批准记录。
	actualSHA := pending.ArgvSHA256(pend.Argv)
	if opts.expectSHA != actualSHA {
		msg := fmt.Sprintf(
			"待办 %q 的 argv 与你批准的指纹不一致: 你批准的指纹=%s; 当前待办实际 argv=%q 指纹=%s; 已拒绝批准(待办文件可能在审查之后被改写)",
			id, opts.expectSHA, pend.Argv, actualSHA)
		auditApproveDenied(id, opts.expectSHA, msg)
		return failRuntime(stdout, stderr, msg)
	}

	// 请求者取自待办文件的 stat 属主, 不取待办内容里的任何字段: 内容可被写者捏造。
	// uid 0 时也必须显式写成 0 —— 缺字段与"root 自己请求的"是两种相反的判定。
	rec := pending.Approval{
		ID:             pend.ID,
		ArgvSHA256:     actualSHA,
		RequestedByUID: &ownerUID,
		RequestedBy:    grantOwnerName(int(ownerUID)),
		ApprovedBy:     approverIdentity,
		ApprovedAt:     now,
	}
	path, err := writeApproval(approvalsDir(), rec)
	if err != nil {
		stampGrant("pending_approve", approveAuditArgs(pend, rec, opts.expectSHA, err.Error()), "error")
		return failRuntime(stdout, stderr, err.Error())
	}
	stampGrant("pending_approve", approveAuditArgs(pend, rec, opts.expectSHA, ""), "success")
	printJSON(stdout, approveDoc{
		ID:         rec.ID,
		Path:       path,
		Argv:       pend.Argv,
		ArgvSHA256: rec.ArgvSHA256,
		ApprovedBy: rec.ApprovedBy,
		ApprovedAt: rec.ApprovedAt.UTC().Format(time.RFC3339),
	})
	return exitOK
}

// pendingEntry 把待办投影成视图; 时间按线上的 RFC3339 输出, 视图与文件读写同一口径。
func pendingEntry(pend *pending.Pending, now time.Time) pendingListEntry {
	return pendingListEntry{
		ID:           pend.ID,
		Kind:         pend.Kind,
		Name:         pend.Name,
		DesiredState: pend.DesiredState,
		Argv:         pend.Argv,
		ArgvSHA256:   pending.ArgvSHA256(pend.Argv),
		Scope:        pend.Scope,
		Reason:       pend.Reason,
		CreatedAt:    pend.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt:    pend.ExpiresAt.UTC().Format(time.RFC3339),
		Expired:      !now.Before(pend.ExpiresAt),
	}
}

// readCappedFile 读文件但拒绝超过上限的内容: 待办目录属主可写, 不设上限就等于
// 让 root 审查时整份读入攻击者指定大小的文件。多读一字节用于判定"超限"。
func readCappedFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPendingFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPendingFileBytes {
		return nil, fmt.Errorf("文件超过 %d 字节上限, 拒绝解析", maxPendingFileBytes)
	}
	return data, nil
}

// fileOwnerUID 取文件的 stat 属主, Lstat 语义: 符号链接与非普通文件一律拒绝 —
// 属主是请求者的唯一权威, 允许间接层等于允许伪造。
func fileOwnerUID(path string) (uint32, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("%s 是符号链接, 拒绝采用其属主", path)
	}
	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("%s 不是普通文件, 拒绝采用其属主", path)
	}
	uid, ok := statFileUID(fi)
	if !ok {
		return 0, fmt.Errorf("无法判定 %s 的属主", path)
	}
	return uid, nil
}

// readPending 读并严格解析一条待办, 同时给出文件的 stat 属主(= 请求者 uid)。
// 文件名与内容 id 必须一致: 路径决定这份文件代表哪条待办, 内容只是它的自述。
func readPending(dir, id string) (*pending.Pending, uint32, error) {
	path, err := pendingFilePath(dir, id)
	if err != nil {
		return nil, 0, err
	}
	data, err := readCappedFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, fmt.Errorf("待办 %q 不存在(%s)", id, path)
		}
		return nil, 0, fmt.Errorf("读取待办 %q 失败: %v", id, err)
	}
	pend, err := pending.ParsePending(data)
	if err != nil {
		return nil, 0, fmt.Errorf("待办 %q 非法: %v", id, err)
	}
	if pend.ID != id {
		return nil, 0, fmt.Errorf("待办 %q 的文件内容 id=%q 与文件名不一致", id, pend.ID)
	}
	ownerUID, err := fileOwnerUID(path)
	if err != nil {
		return nil, 0, fmt.Errorf("待办 %q 的属主无法采信: %v", id, err)
	}
	return pend, ownerUID, nil
}

// pendingDir / approvalsDir 解析目录: env 覆盖优先, 否则生产位置。
func pendingDir() string {
	if v := os.Getenv(envPendingDir); v != "" {
		return v
	}
	return defaultPendingDir
}

func approvalsDir() string {
	if v := os.Getenv(envApprovalsDir); v != "" {
		return v
	}
	return defaultApprovalsDir
}

// pendingFilePath 拼出待办文件路径并复核目录包含: id 来自命令行, 绝不许逃出目录。
func pendingFilePath(dir, id string) (string, error) {
	if err := pending.ValidateID(id); err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+".json")
	if filepath.Dir(path) != filepath.Clean(dir) {
		return "", fmt.Errorf("待办 id %q 非法(解析后逃出待办目录)", id)
	}
	return path, nil
}

// approvalsDirUID 读目录属主 uid; 独立成函数是为了让测试能钉住"非 root 属主"分支
// (非 root 的测试进程造不出 root 属主目录)。ok=false 表示无法判定 ⇒ fail-closed。
var approvalsDirUID = statFileUID

// statFileUID 从 Lstat 的 FileInfo 取属主 uid。
func statFileUID(fi os.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

// checkApprovalsDir 是批准目录的纯判定: 必须是真实目录(非符号链接)、属主 root、
// 组/其他不可写。任一条不成立, "只有 root 能写"就只是口号, 此时拒绝批准。
func checkApprovalsDir(path string, fi os.FileInfo) error {
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("批准路径 %s 是符号链接, 拒绝批准(批准目录必须是真实的 0755 root:root 目录)", path)
	}
	if !fi.IsDir() {
		return fmt.Errorf("批准路径 %s 不是目录", path)
	}
	uid, ok := approvalsDirUID(fi)
	if !ok {
		return fmt.Errorf("无法判定批准目录 %s 的属主, 拒绝批准", path)
	}
	if uid != 0 {
		return fmt.Errorf("批准目录 %s 的属主是 uid=%d, 不是 root; 批准目录必须属于 root(否则属主自己能伪造批准记录)", path, uid)
	}
	if perm := fi.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("批准目录 %s 对其他用户可写(mode=%o), 闸门形同虚设; 请修正为 0755 root:root", path, perm)
	}
	return nil
}

// ensureApprovalsDir 确认批准目录可用: 不存在则按生产权限建, 存在则要求它是
// 真实的 root 属主目录 —— 目录一旦能被非 root 写/拥有, 这道门就等于不存在。
func ensureApprovalsDir(dir string) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建批准目录 %s 失败: %v", dir, err)
		}
		// MkdirAll 的 mode 会被 umask 削减: umask 077 的 root 会建出 0700, 消费侧
		// 读不到批准记录, 表现为"批准成功但永远执行不了"。显式 chmod, 失败即拒。
		if err := os.Chmod(dir, 0o755); err != nil {
			return fmt.Errorf("设置批准目录 %s 权限失败: %v", dir, err)
		}
		fi, err = os.Lstat(dir)
	}
	if err != nil {
		return fmt.Errorf("读取批准目录 %s 失败: %v", dir, err)
	}
	return checkApprovalsDir(dir, fi)
}

// writeApproval 原子写入批准记录(临时文件 → fsync → rename): 半份记录永远不会
// 被消费侧读到。属主天然是调用者(root) —— 非 root 连目录都写不进来。
func writeApproval(dir string, rec pending.Approval) (string, error) {
	if err := ensureApprovalsDir(dir); err != nil {
		return "", err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return "", fmt.Errorf("序列化批准记录失败: %v", err)
	}
	tmp, err := os.CreateTemp(dir, ".approval-*.tmp")
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
		return "", fmt.Errorf("写批准记录失败: %v", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("设置批准记录权限失败: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("flush 批准记录失败: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("关闭批准记录临时文件失败: %v", err)
	}
	final := filepath.Join(dir, rec.ID+".json")
	if err := os.Rename(tmpPath, final); err != nil {
		return "", fmt.Errorf("落位批准记录 %s 失败: %v", final, err)
	}
	renamed = true
	return final, nil
}

// approveAuditArgs 是批准审计的 args 最小集: 被批准的动作、双方指纹与请求者必须可回溯。
func approveAuditArgs(pend *pending.Pending, rec pending.Approval, expectSHA, errMsg string) map[string]any {
	args := map[string]any{
		"pending_id":    pend.ID,
		"kind":          pend.Kind,
		"name":          pend.Name,
		"desired_state": pend.DesiredState,
		"scope":         pend.Scope,
		"argv_sha256":   rec.ArgvSHA256,
		"expect_sha256": expectSHA,
		"approved_by":   rec.ApprovedBy,
	}
	if rec.RequestedByUID != nil {
		args["requested_by_uid"] = *rec.RequestedByUID
	}
	if errMsg != "" {
		args["error"] = errMsg
	}
	return args
}

// auditApproveDenied 记一条拒绝证据: 批准被拒也是这道门的运行事实, 必须留痕。
// 不写 approved_by —— 拒绝不是批准, 写了会让事后审计误判"root 批过"。
func auditApproveDenied(id, expectSHA, msg string) {
	args := map[string]any{
		"pending_id": id,
		"error":      msg,
	}
	if expectSHA != "" {
		args["expect_sha256"] = expectSHA
	}
	stampGrant("pending_approve", args, "denied")
}
