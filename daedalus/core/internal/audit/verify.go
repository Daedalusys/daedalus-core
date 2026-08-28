package audit

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// requiredFields 是一条合法记录必须包含且必须为字符串的字段
// (args 单独处理: 任意 JSON 类型均合法)。
var requiredFields = []string{
	"timestamp", "identity", "tool", "policy_version",
	"outcome", "prev_hash", "entry_hash",
}

// Verify 全链重算校验 logPath 的哈希链, 是证据边界的完整性证明入口。
//
// 语义(比追加路径 get_last_entry_hash 更严格, 这是证据层的应有姿态):
//  1. 逐行: 跳过空行; 任一非空行不可解析/缺字段/字段类型错 → 报损坏;
//  2. 首条有效记录的 prev_hash 必须为 GenesisHash;
//  3. 每条记录的 prev_hash 必须等于上一条的 entry_hash(链连续性);
//  4. 每条记录的 entry_hash 必须等于按 §4.3 载荷重算的哈希(防篡改)。
//
// 返回校验通过的记录条数; 任一环节失败返回带行号的 error。
func Verify(logPath string) (int, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return 0, fmt.Errorf("audit: 打开日志失败: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // 审计行可能含长 args, 上限 4MiB

	prev := GenesisHash
	count := 0
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		v, perr := ParseValue(line)
		if perr != nil {
			return count, fmt.Errorf("audit: 第 %d 行损坏: 非法 JSON", lineNo)
		}
		if !v.IsObject() {
			return count, fmt.Errorf("audit: 第 %d 行损坏: 记录不是 JSON 对象", lineNo)
		}
		fields := make(map[string]string, len(requiredFields))
		for _, name := range requiredFields {
			s, ok := v.LookupString(name)
			if !ok {
				return count, fmt.Errorf("audit: 第 %d 行损坏: 字段 %s 缺失或非字符串", lineNo, name)
			}
			fields[name] = s
		}
		args, ok := v.Lookup("args")
		if !ok {
			return count, fmt.Errorf("audit: 第 %d 行损坏: 字段 args 缺失", lineNo)
		}
		if fields["prev_hash"] != prev {
			return count, fmt.Errorf(
				"audit: 第 %d 行链断裂: prev_hash=%s, 期望 %s", lineNo, fields["prev_hash"], prev)
		}
		recomputed := ComputeEntryHash(
			fields["timestamp"], fields["identity"], fields["tool"],
			args.ArgsString(), fields["outcome"], fields["prev_hash"])
		if recomputed != fields["entry_hash"] {
			return count, fmt.Errorf(
				"audit: 第 %d 行哈希不符: entry_hash=%s, 重算=%s", lineNo, fields["entry_hash"], recomputed)
		}
		prev = fields["entry_hash"]
		count++
	}
	if err := sc.Err(); err != nil {
		return count, fmt.Errorf("audit: 读取日志失败: %w", err)
	}
	return count, nil
}
