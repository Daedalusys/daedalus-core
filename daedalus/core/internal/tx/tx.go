// 事务(tx)生命周期与日志(journal)原语 —— AIOS 对象模型 C3 核心(todo 14)。
//
// 职责边界(与后续消费者的分工):
//   - 本包只做状态机 + 持久化 + 回滚计划生成;不执行任何适配器动作
//     (service.set 的快照/应用/回滚在 todo 22),不写审计(todo 15 的 CLI 负责打桩),
//     不做网络 I/O,不 spawn 任何子进程。
//   - daedalus-tx CLI(todo 15)消费本包的 Begin/Load/Append/Mark*/BuildRollbackPlan;
//     JSON 形态(OpResult 四键、Step 六键、Transaction 五键)是为 T15/T22/T23
//     的 JSON 消费者钉死的线上契约,改动即跨 todo 破坏。
//
// 持久化模型:每个事务一个 JSON 文件 `<root>/<tx-id>.json`,root 由
// internal/dirs.TxRoot() 解析(env DAEDALUS_TX_DIR → /var/lib/daedalus/tx →
// $HOME/.local/share/daedalus/tx),本包绝不复制任何路径字面量。
// 整个 Transaction 序列化为一个 JSON 文档,在 flock(LOCK_EX) 下整体重写
// (镜像 audit.go:125-136 的加锁纪律:O_RDWR|O_CREATE 打开 → LOCK_EX →
// defer 注册晚于 Close → LIFO 退出时先 LOCK_UN 再 Close,保证链式写无竞态、
// 文件内容永远是完整文档而不是撕裂的半截)。
//
// 路径安全(round-2/3 fold 钉死,三道防线按序生效):
//  1. 形状门:tx-id 必须匹配 ^[a-f0-9]{16}$(crypto/rand 8 字节小写十六进制)。
//     Begin 用自带生成器 + 同款正则自检(纵深防御:生成器若被改坏,正则仍拦截);
//     Load/journalPath 对 CLI 传入的任意 id 先过同一道门,**通过后才允许
//     filepath.Join 构造路径**——`../`、绝对路径、空字节、大写十六进制在
//     拼接之前就已被拒绝,正则本身即排除任何路径分隔符。
//  2. realpath 包含性门:journal 文件的 realpath 必须位于 root 的 realpath
//     之下(目录段逐字节相等)。落盘/读取之前先断言,不信任 join 的字面结果:
//     root 被符号链接换址、既有日志文件被换成指向外部的符号链接,都会在这里
//     被拒,且发生在任何 fs 写之前。
//  3. 文件名唯一性门:Begin 以 O_CREATE|O_EXCL 创建日志(同 id 二次创建 →
//     ErrAlreadyExists,绝不覆盖既有事务),配合 16 字节随机 id 使碰撞概率
//     低于任何可攻击窗口。
package tx

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/daedalus-os/daedalus/core/internal/dirs"
)

// Status 是事务生命周期状态,小写线协议 token 与 JSON 消费者(T15 status 输出)逐字节一致。
type Status string

// 状态全集。语义:
//   - proposed   : Begin 后的初始态,可继续 Append;尚未产生任何副作用。
//   - applying   : apply 执行中(steps 冻结,只允许向 applied/failed 迁移)。
//   - applied    : 全部步骤成功;回滚计划已固化,唯一合法去向 rolled_back。
//   - rolled_back: 回滚计划执行完毕,终态。
//   - failed     : applying 中某步失败(或 proposed 期主动放弃),终态。
const (
	StatusProposed   Status = "proposed"
	StatusApplying   Status = "applying"
	StatusApplied    Status = "applied"
	StatusRolledBack Status = "rolled_back"
	StatusFailed     Status = "failed"
)

// 状态迁移表(唯一权威;中文表供评审对照,新增状态必须同步此表与 tx_test 的全表测试):
//
//	from \ to      applying  applied  rolled_back  failed
//	proposed         ✔        ✘        ✘          ✔   (proposed→failed = apply 前主动放弃)
//	applying         ✘        ✔        ✘          ✔   (某步失败)
//	applied          ✘        ✘        ✔          ✘   (回滚)
//	rolled_back      ✘        ✘        ✘          ✘   (终态)
//	failed           ✘        ✘        ✘          ✘   (终态)
//
// 表外一切迁移(含 MarkApplied 跳过 applying 直达)均为 ErrInvalidTransition。
var legalTransitions = map[Status]map[Status]bool{
	StatusProposed: {StatusApplying: true, StatusFailed: true},
	StatusApplying: {StatusApplied: true, StatusFailed: true},
	StatusApplied:  {StatusRolledBack: true},
}

// 哨兵错误:CLI(todo 15)用 errors.Is 区分退出码(非法 id → exit 2 用法错;
// 未找到 → exit 1;非法迁移 → exit 1 状态冲突)。
var (
	// ErrInvalidID 表示 tx-id 未通过 ^[a-f0-9]{16}$ 形状门(路径安全第 1 道防线)。
	ErrInvalidID = errors.New("tx: 事务 ID 非法")
	// ErrNotFound 表示日志文件不存在(CLI 据此报 "transaction <id> not found")。
	ErrNotFound = errors.New("tx: 事务日志不存在")
	// ErrAlreadyExists 表示同 id 日志已存在(Begin 幂等防线:绝不覆盖)。
	ErrAlreadyExists = errors.New("tx: 事务日志已存在")
	// ErrInvalidTransition 表示迁移不在状态迁移表内。
	ErrInvalidTransition = errors.New("tx: 非法状态迁移")
)

// txIDPattern 钉死 tx-id 形状:crypto/rand 8 字节 → 16 位小写十六进制。
// 该正则同时排除 `/`、`\`、空字节、`..`、大写与前缀多余字符——
// journalPath 在拼接之前先过这道门(round-2 fold 路径安全)。
var txIDPattern = regexp.MustCompile(`^[a-f0-9]{16}$`)

// journalFileMode 是日志文件权限,与审计日志惯例一致(内容非机密,0644)。
const journalFileMode = 0o644

// OpResult 是步骤执行的规范化结果,JSON 键为 T15/T22/T23 钉死的线上契约
// (集成测试断言 OpResult.returncode;copilot exec.ts 解析 returncode/error)。
type OpResult struct {
	Returncode int    `json:"returncode"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Step 是事务中的一个适配器步骤。Args/BeforeState/AfterState 是适配器领域
// (tx 层不理解内容,只负责持久化与排序);Index 由 Append 自动置为当前步数。
type Step struct {
	Index       int             `json:"index"`
	Adapter     string          `json:"adapter"`
	Args        json.RawMessage `json:"args,omitempty"`
	BeforeState json.RawMessage `json:"before_state,omitempty"`
	AfterState  json.RawMessage `json:"after_state,omitempty"`
	// OpResult 恒发射(不用 omitzero):已提议但未执行的步骤呈
	// {"returncode":0},消费者可依赖 op_result 键始终存在。
	OpResult OpResult `json:"op_result"`
}

// RollbackPlan 是逆序的回滚步骤集合;生成规则见 rollback.go。
type RollbackPlan struct {
	Steps []Step `json:"steps,omitempty"`
}

// Transaction 是一个事务的完整内存态,序列化后即日志文件全文(JSON 五键为
// T15 status 子命令的线上契约)。未导出字段(journalPath/互斥锁)不参与序列化。
type Transaction struct {
	ID           string       `json:"id"`
	CreatedAt    time.Time    `json:"created_at"`
	Status       Status       `json:"status"`
	Steps        []Step       `json:"steps,omitempty"`
	RollbackPlan RollbackPlan `json:"rollback_plan"`

	// journalPath 是构造期即钉死的日志路径(已过三道路径安全门)。
	journalPath string
	// mu 串行化同进程内对同一 *Transaction 的并发 Append/Mark*(跨进程由 flock 兜底)。
	mu sync.Mutex
}

// Begin 生成新事务:随机 id(自检正则)→ dirs 解析日志根 → journalPath 构造 +
// 包含性断言 → O_EXCL 创建 + flock 写入初始 proposed 态。任何一步失败都不留半成品。
func Begin() (*Transaction, error) {
	id, err := newTxID()
	if err != nil {
		return nil, err
	}
	return beginWithID(id)
}

// beginWithID 是 Begin 的可注入 id 内核,同时充当同 id 幂等防线的测试缝:
// 日志已存在 → ErrAlreadyExists(绝不覆盖既有事务)。
func beginWithID(id string) (*Transaction, error) {
	root, err := dirs.TxRoot()
	if err != nil {
		return nil, err
	}
	path, err := journalPath(root, id)
	if err != nil {
		return nil, err
	}
	t := &Transaction{
		ID:          id,
		CreatedAt:   time.Now().UTC(),
		Status:      StatusProposed,
		journalPath: path,
	}
	if err := t.create(); err != nil {
		return nil, err
	}
	return t, nil
}

// Load 按 id 读取日志:先过与 Begin 相同的 id 形状门(journalPath 内含),
// 再在共享锁(LOCK_SH)下读取,内容 id 与文件名 id 必须逐字节一致
// (防文件被掉包);状态非法 → 拒绝(日志被手改即 fail-closed)。
func Load(id string) (*Transaction, error) {
	root, err := dirs.TxRoot()
	if err != nil {
		return nil, err
	}
	path, err := journalPath(root, id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, fmt.Errorf("tx: 打开日志失败: %w", err)
	}
	defer f.Close()
	// 读侧取共享锁:与写侧 LOCK_EX 互斥,保证解析到的永远是完整文档。
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		return nil, fmt.Errorf("tx: flock(LOCK_SH) 失败: %w", err)
	}
	// defer LIFO:先 LOCK_UN 再 Close(与 audit 同纪律)。
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("tx: 读取日志失败: %w", err)
	}
	var t Transaction
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("tx: 日志 %s 解析失败: %w", id, err)
	}
	if t.ID != id {
		return nil, fmt.Errorf("tx: 日志内容 id %q 与文件名 id %q 不一致(疑似掉包)", t.ID, id)
	}
	if !validStatus(t.Status) {
		return nil, fmt.Errorf("tx: 日志 %s 含未知状态 %q", id, t.Status)
	}
	t.journalPath = path
	return &t, nil
}

// Append 追加一个步骤,仅允许在 proposed 态(steps 在 applying 起冻结)。
// 校验次序:状态门 → Adapter 非空 → Index 非负 → 自动置 Index=len(Steps)
// (覆盖调用方传值,序号权威在 tx 层)→ flock 下整体重写;写失败回滚内存态。
func (t *Transaction) Append(step Step) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.Status != StatusProposed {
		return fmt.Errorf("tx: 仅 %s 态可追加步骤(当前 %s)", StatusProposed, t.Status)
	}
	if step.Adapter == "" {
		return errors.New("tx: 步骤 Adapter 不得为空")
	}
	if step.Index < 0 {
		return fmt.Errorf("tx: 步骤 Index 不得为负: %d", step.Index)
	}
	step.Index = len(t.Steps)
	t.Steps = append(t.Steps, step)
	if err := t.save(); err != nil {
		t.Steps = t.Steps[:len(t.Steps)-1]
		return err
	}
	return nil
}

// MarkApplying / MarkApplied / MarkRolledBack / MarkFailed 按状态迁移表推进
// 生命周期并持久化;非法迁移或写失败均保持原状态(内存与日志不分叉)。
func (t *Transaction) MarkApplying() error { return t.mark(StatusApplying) }
func (t *Transaction) MarkApplied() error  { return t.mark(StatusApplied) }

// MarkRolledBack 完成回滚后调用;MarkFailed 供 applying 中某步失败或
// proposed 阶段主动放弃(见状态迁移表)。
func (t *Transaction) MarkRolledBack() error { return t.mark(StatusRolledBack) }
func (t *Transaction) MarkFailed() error     { return t.mark(StatusFailed) }

// mark 是所有状态迁移的唯一入口(表驱动,表外即拒)。置为 applied 时顺带
// 固化回滚计划(BuildRollbackPlan 纯函数,适配器逆向动作属 todo 22)。
func (t *Transaction) mark(to Status) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !legalTransitions[t.Status][to] {
		return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, t.Status, to)
	}
	from, oldPlan := t.Status, t.RollbackPlan
	t.Status = to
	if to == StatusApplied {
		t.RollbackPlan = BuildRollbackPlan(t.Steps)
	}
	if err := t.save(); err != nil {
		t.Status, t.RollbackPlan = from, oldPlan
		return err
	}
	return nil
}

// ──── 路径安全与持久化内核 ────

// newTxID 用 crypto/rand 生成 8 字节小写十六进制 id,并过同款正则自检
// (纵深防御:即使生成逻辑被改坏,非法形状也绝不出门)。
func newTxID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("tx: 生成事务 ID 失败: %w", err)
	}
	id := hex.EncodeToString(b[:])
	if !txIDPattern.MatchString(id) {
		return "", fmt.Errorf("%w: 自生成 ID 未通过正则防线: %q", ErrInvalidID, id)
	}
	return id, nil
}

// journalPath 构造并校验 `<root>/<id>.json`:
//  1. id 先过形状门(不过则**绝不**拼接路径);
//  2. root 取 realpath(dirs 探测后 root 必存在);
//  3. 文件已存在时其 realpath 的父目录必须逐字节等于 root realpath(符号链接
//     逃逸在此拒绝);新建场景由第 1 道门保证名字无分隔符,天然在 root 下。
//
// 返回的是未解析的落盘路径(与 root 同一命名空间),检查通过后才允许任何 fs 写。
func journalPath(root, id string) (string, error) {
	if !txIDPattern.MatchString(id) {
		return "", fmt.Errorf("%w: %q(需匹配 %s)", ErrInvalidID, id, txIDPattern.String())
	}
	path := filepath.Join(root, id+".json")

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("tx: 解析日志根 realpath 失败: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		realPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("tx: 解析日志路径 realpath 失败: %w", err)
		}
		if filepath.Dir(realPath) != realRoot {
			return "", fmt.Errorf("tx: 日志路径逃逸根目录(包含性检查失败): %q", id)
		}
	}
	return path, nil
}

// create 以 O_EXCL 独占创建日志并写入初始态(幂等防线,见 beginWithID 注释);
// save 整体重写日志。两者仅由持 t.mu 的调用路径使用(进程内一致性由互斥锁保证),
// 跨进程串行交给文件 flock。
func (t *Transaction) create() error { return t.persist(os.O_RDWR | os.O_CREATE | os.O_EXCL) }
func (t *Transaction) save() error   { return t.persist(os.O_RDWR | os.O_CREATE) }

// persist 以指定 flags 打开日志并在 LOCK_EX 下重写全文。加锁纪律镜像
// audit.go:145-156:open → LOCK_EX → defer 注册晚于 Close → LIFO 退出时先
// LOCK_UN 再 Close;锁加在文件自身 fd 上,跨进程互斥,保证文件内容永远是
// 完整 JSON 文档(撕裂写不可能;O_EXCL 新建场景下截断空文件是恒等操作,
// 截断只发生在持锁之后,读者绝看不到半截文档)。
// O_EXCL 命中已存在文件 → ErrAlreadyExists(Begin 幂等防线,绝不覆盖)。
func (t *Transaction) persist(flags int) error {
	f, err := os.OpenFile(t.journalPath, flags, journalFileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrAlreadyExists, t.ID)
		}
		return fmt.Errorf("tx: 打开日志失败: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("tx: flock(LOCK_EX) 失败: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("tx: 截断日志失败: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("tx: 定位日志失败: %w", err)
	}
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("tx: 序列化事务失败: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("tx: 写入日志失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("tx: flush 日志失败: %w", err)
	}
	return nil
}

// validStatus 判定状态 token 是否在全集内(Load 的 fail-closed 内容门)。
func validStatus(s Status) bool {
	switch s {
	case StatusProposed, StatusApplying, StatusApplied, StatusRolledBack, StatusFailed:
		return true
	default:
		return false
	}
}
