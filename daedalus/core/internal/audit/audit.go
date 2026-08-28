package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// 哈希链与默认值常量, 与 audit-log.py:16-18 逐一对应。
const (
	// GenesisHash 是创世 prev_hash(空文件/无有效尾行时使用), Python GENESIS_HASH = "0"*64。
	GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"
	// DefaultPolicyVersion 对应 Python POLICY_VERSION = "1.0"。
	DefaultPolicyVersion = "1.0"
	// EnvLogPath 对应 Python AUDIT_LOG_PATH 的环境变量覆盖。
	EnvLogPath = "DAEDALUS_AUDIT_LOG_PATH"
	// systemLogPath 对应 Python 的 /var/log/daedalus/audit.jsonl 默认值。
	systemLogPath = "/var/log/daedalus/audit.jsonl"
	// tailChunkSize 对应 Python get_last_entry_hash 的 buffer_size = 4096。
	tailChunkSize = 4096
)

// DefaultLogPath 复刻 audit-log.py:16: 环境变量优先, 否则系统默认路径。
// (argparse 的 --log-path 默认值即该常量, 显式旗标仍可覆盖环境变量。)
func DefaultLogPath() string {
	if v := os.Getenv(EnvLogPath); v != "" {
		return v
	}
	return systemLogPath
}

// ComputeEntryHash 计算审计条目哈希, 等价 audit-log.py:21-37 的 compute_entry_hash。
//
// args 参数传**已规范化**的 args_str(由 Value.ArgsString 产出, 语义等价 Python 端
// 接收 args 后内部的 json.dumps 规范化); 载荷拼接顺序严禁改动:
//
//	payload = timestamp + identity + tool + args_str + outcome + prev_hash
//	entry_hash = SHA-256(payload.encode("utf-8")).hexdigest()   # 小写十六进制
func ComputeEntryHash(timestamp, identity, tool, args, outcome, prevHash string) string {
	payload := timestamp + identity + tool + args + outcome + prevHash
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// Record 是一条完整审计记录, 字段与 audit-log.py:133-142 的 record dict 一致。
type Record struct {
	Timestamp     string
	Identity      string
	Tool          string
	Args          *Value
	PolicyVersion string
	Outcome       string
	PrevHash      string
	EntryHash     string
}

// toValue 按 Python dict 插入序组装记录对象(stdout 依赖该字段序)。
func (r *Record) toValue() *Value {
	v := NewObject()
	v.set("timestamp", NewString(r.Timestamp))
	v.set("identity", NewString(r.Identity))
	v.set("tool", NewString(r.Tool))
	v.set("args", r.Args)
	v.set("policy_version", NewString(r.PolicyVersion))
	v.set("outcome", NewString(r.Outcome))
	v.set("prev_hash", NewString(r.PrevHash))
	v.set("entry_hash", NewString(r.EntryHash))
	return v
}

// Line 返回日志文件中的一行(不含换行符), 等价 json.dumps(record, sort_keys=True)。
func (r *Record) Line() string {
	return encodeValue(r.toValue(), modeLine).String()
}

// IndentJSON 返回 CLI stdout 的格式化记录, 等价 json.dumps(record, indent=2):
// 不排序(保持字段插入序), 嵌套 args 保持输入文档键序。
func (r *Record) IndentJSON() string {
	return encodeValue(r.toValue(), modeStdout).String()
}

// Entry 是 LogAudit 的输入(参数 >3 个的字段聚合为类型化值对象)。
type Entry struct {
	Identity      string // 调用者 ID(默认由 CLI 填 "cli")
	Tool          string // 工具名, 必填
	Args          *Value // nil 视为 {} (audit-log.py:106-107)
	Outcome       string // "" 视为 "success"
	PolicyVersion string // "" 视为 DefaultPolicyVersion
	LogPath       string // "" 视为 DefaultLogPath()
}

// LogAudit 追加一条哈希链审计条目, 等价 audit-log.py:85-150 的 log_audit。
//
// 并发协议: 打开(a+ 等价 O_RDWR|O_CREATE|O_APPEND) → flock(LOCK_EX) →
// 读尾行 prev_hash → 写行 → flush → LOCK_UN(由 defer 在 Close 前释放)。
// 锁加在**日志文件本身**的 fd 上, 与 Python fcntl.flock(f.fileno(), LOCK_EX) 同语义,
// 与外部 Python 写入方互斥, 保证链计算无竞态。
func LogAudit(e Entry) (*Record, error) {
	if e.Args == nil {
		e.Args = NewObject()
	}
	if e.Outcome == "" {
		e.Outcome = "success"
	}
	if e.PolicyVersion == "" {
		e.PolicyVersion = DefaultPolicyVersion
	}
	if e.LogPath == "" {
		e.LogPath = DefaultLogPath()
	}

	// audit-log.py:109-114: 目录不存在则尽力创建, 失败静默忽略(留给 open 报错)。
	if dir := filepath.Dir(e.LogPath); dir != "" {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			_ = os.MkdirAll(dir, 0o755)
		}
	}

	f, err := os.OpenFile(e.LogPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("audit: 打开日志文件失败: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("audit: flock(LOCK_EX) 失败: %w", err)
	}
	// defer 为 LIFO: 本行注册晚于上面的 Close → 退出时先 LOCK_UN 再 Close,
	// 与 Python try/finally 中"写完后 LOCK_UN、随 with 块关闭文件"顺序一致。
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	prevHash := lastEntryHash(f)
	timestamp := FormatTimestamp(time.Now())
	entryHash := ComputeEntryHash(timestamp, e.Identity, e.Tool, e.Args.ArgsString(), e.Outcome, prevHash)

	rec := &Record{
		Timestamp:     timestamp,
		Identity:      e.Identity,
		Tool:          e.Tool,
		Args:          e.Args,
		PolicyVersion: e.PolicyVersion,
		Outcome:       e.Outcome,
		PrevHash:      prevHash,
		EntryHash:     entryHash,
	}

	// a+ 模式下写恒追加; 与 Python f.seek(0, SEEK_END) 等效的防御性定位。
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return nil, fmt.Errorf("audit: 定位文件末尾失败: %w", err)
	}
	if _, err := f.WriteString(rec.Line() + "\n"); err != nil {
		return nil, fmt.Errorf("audit: 写入日志行失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("audit: flush 失败: %w", err)
	}
	return rec, nil
}

// FormatTimestamp 复刻 datetime.datetime.now(datetime.timezone.utc).isoformat()。
//
// 输出 "YYYY-MM-DDTHH:MM:SS.ffffff+00:00"; **微秒为 0 时 Python 省略整个小数秒段**
// (audit-log.py:122 的隐性契约, 断链高发点, 单独成函数便于测试该怪癖)。
// 纳秒按 Python 习惯向零截断为微秒, 不做四舍五入。
func FormatTimestamp(t time.Time) string {
	t = t.UTC()
	base := t.Format("2006-01-02T15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		base += fmt.Sprintf(".%06d", us)
	}
	return base + "+00:00"
}

// lastEntryHash 镜像 audit-log.py:40-82 的 get_last_entry_hash(f):
// 自文件末尾按 4096 字节块回溯, 收集"最后一个换行边界块"内的行,
// 倒序找第一条非空且可解析为对象、含 entry_hash 的行; 找不到返回 GenesisHash。
//
// 回溯算法逐行复刻(含"块首行被截断则解析失败跳过"的 Python 同源行为),
// 保证两侧对畸形/超大尾行的 prev_hash 判定完全一致。
func lastEntryHash(f *os.File) string {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil || size == 0 {
		return GenesisHash
	}

	offset := size
	var lines []string
	residual := ""

	for offset > 0 && len(lines) < 2 {
		readSize := int64(tailChunkSize)
		if readSize > offset {
			readSize = offset
		}
		offset -= readSize
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return GenesisHash
		}
		chunk := make([]byte, readSize)
		n, err := io.ReadFull(f, chunk)
		if err != nil && n == 0 {
			return GenesisHash
		}
		chunkCombined := string(chunk[:n]) + residual
		if split := pySplitLines(chunkCombined); len(split) > 1 {
			lines = split
			break
		}
		residual = chunkCombined
	}
	if len(lines) == 0 && residual != "" {
		lines = []string{residual}
	}

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		v, err := ParseValue(line)
		if err != nil {
			continue
		}
		if !v.IsObject() {
			continue
		}
		// Python: return str(data["entry_hash"]) —— 仅字符串 kind 与哈希链实态一致,
		// 其余类型视为损坏并继续向更早行回溯。
		if eh, ok := v.LookupString("entry_hash"); ok {
			return eh
		}
	}
	return GenesisHash
}

// pySplitLines 近似 Python str.splitlines: 以 \n / \r\n / \r 断行且不产出尾部空行。
// (Python 还按 \v \f U+0085 U+2028 等断行, 但审计行经 ensure_ascii 序列化后
// 这些字符只可能以 \uXXXX 转义形态出现, 原文中永不出现裸字节, 故三者已完备。)
func pySplitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' && s[i] != '\r' {
			continue
		}
		out = append(out, s[start:i])
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			i++
		}
		start = i + 1
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
