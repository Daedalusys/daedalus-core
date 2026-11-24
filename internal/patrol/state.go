// state.go 定义跨轮状态的读写:状态文件(按节点分文件)与本轮发现文件(JSONL)。
//
// 落盘一律「临时文件 → fsync → rename」整体替换:读侧要么看到旧版本要么看到新版本,
// 不会读到半截文件。
package patrol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LoadState 读上一轮的活跃发现。状态文件按节点分文件存放,键是 item_id。
// 文件不存在等于「首轮还没有状态」,不是错误;文件损坏一律报错 ——
// 把坏状态当成空状态会让下一轮把老问题全报成新增。
func LoadState(path string) (map[string]Finding, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Finding{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取巡检状态 %s 失败: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]Finding{}, nil
	}
	var st map[string]Finding
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("解析巡检状态 %s 失败: %w", path, err)
	}
	if st == nil {
		st = map[string]Finding{}
	}
	// 键必须与条目自报的 item_id 一致:手改过或半截写入的文件不带进下一轮。
	for id, f := range st {
		if f.ItemID != id {
			return nil, fmt.Errorf("巡检状态 %s 的键 %q 与条目 item_id %q 不一致", path, id, f.ItemID)
		}
	}
	return st, nil
}

// SaveState 整体替换状态文件。上一轮的键不会残留 —— 状态是「本轮活跃项」的快照,
// 不是逐轮追加的日志。
func SaveState(path string, st map[string]Finding) error {
	if st == nil {
		// nil map 序列化成 null 太含糊:落成 {} 才和「本轮没有活跃项」一一对应。
		st = map[string]Finding{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化巡检状态失败: %w", err)
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// ReadFindings 读一轮的发现(JSONL,一行一条)。文件不存在等于「本轮没有发现」;
// 任何一行坏掉都报错 —— 静默跳过等于把发现吞掉。
func ReadFindings(path string) ([]Finding, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取发现文件 %s 失败: %w", path, err)
	}
	var out []Finding
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var f Finding
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			return nil, fmt.Errorf("解析发现文件 %s 第 %d 行失败: %w", path, i+1, err)
		}
		// 身份缺失的发现没法参与跨轮去重,直接拒。
		if f.ItemID == "" {
			return nil, fmt.Errorf("发现文件 %s 第 %d 行缺 item_id", path, i+1)
		}
		out = append(out, f)
	}
	return out, nil
}

// WriteFindings 写一轮的发现(JSONL,一行一条),整体原子替换。
func WriteFindings(path string, findings []Finding) error {
	var buf bytes.Buffer
	for _, f := range findings {
		line, err := json.Marshal(f)
		if err != nil {
			return fmt.Errorf("序列化发现失败: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return writeFileAtomic(path, buf.Bytes())
}

// writeFileAtomic 落一份完整文件:同目录临时文件 → fsync → rename。
// 父目录一并建出来,调用方不必先 mkdir。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("在 %s 创建临时文件失败: %w", dir, err)
	}
	tmpName := tmp.Name()
	// rename 成功后这次 Remove 只是静默失败;失败路径上它负责清掉残留临时文件。
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入 %s 失败: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("同步 %s 失败: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭 %s 失败: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换 %s 失败: %w", path, err)
	}
	return nil
}
