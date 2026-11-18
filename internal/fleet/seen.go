// internal/fleet/seen.go:节点 last_seen 的观测侧车(seen.log)。
//
// last_seen 是纯观测值(全仓没有判定读它),所以从身份表挪出来:markSeen 只往
// 这个 append-only 文件追加一行,身份表(nodes.jsonl)的整表原子替换与它无关。
// 读侧把侧车折叠到身份表之上(同节点最后一条胜),Put/Delete 时顺带折叠进身份表
// 并清空侧车;行数越过阈值就原子压实。
//
// durability 刻意降级:追加**不 fsync**。崩溃只丢最近的 last_seen 增量,节点身份
// 不受影响 —— 不要在这里补 Sync,那等于把每次下发都拖回等盘的老路。读侧只容忍
// **末尾**半截行(不 fsync 的唯一产物);中间行坏按真损坏 fail-closed。
package fleet

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// seenFileName 是 last_seen 观测侧车的文件名。
	seenFileName = "seen.log"
	// seenCompactFactor / seenCompactFloor 定义压实阈值:行数超过
	// max(factor×节点数, floor) 才压实。floor 兜住小集群的抖动成本 ——
	// 否则几台机来回下发就会把压实(`读 + 重写`)变成新的热路径。
	seenCompactFactor = 4
	seenCompactFloor  = 1024
)

// seenEntry 是侧车的一行:只记身份键与观测值,不重复身份表的任何字段。
type seenEntry struct {
	NodeID       string `json:"node_id"`
	LastSeenUnix int64  `json:"last_seen_unix"`
}

// seenPath 返回观测侧车文件路径。
func (r *Registry) seenPath() string {
	return filepath.Join(r.root, seenFileName)
}

// appendSeen 追加一条观测。一次 Write 写完整行(含换行),不 Sync:
// 半截行的唯一来源是崩溃,由读侧容忍末尾半截行兜住。
func (r *Registry) appendSeen(nodeID string, unix int64) error {
	b, err := json.Marshal(seenEntry{NodeID: nodeID, LastSeenUnix: unix})
	if err != nil {
		return fmt.Errorf("fleet: 序列化 last_seen 失败: %w", err)
	}
	f, err := os.OpenFile(r.seenPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("fleet: 打开观测侧车 %s 失败: %w", r.seenPath(), err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("fleet: 追加 last_seen 失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("fleet: 关闭观测侧车失败: %w", err)
	}
	return nil
}

// loadSeen 扫描侧车,返回 node_id → 最后一次观测值。同一节点后写的覆盖先写的
// (取最后一条而不是最大值:时钟回拨时"最后一次观测"才是真相)。
func (r *Registry) loadSeen() (map[string]int64, error) {
	raw, err := os.ReadFile(r.seenPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]int64{}, nil
		}
		return nil, fmt.Errorf("fleet: 读取观测侧车 %s 失败: %w", r.seenPath(), err)
	}

	seen := make(map[string]int64)
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		raw := strings.TrimSpace(line)
		if raw == "" {
			continue
		}
		var e seenEntry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			// 末尾半截行只可能是崩溃时没写完的那一条,跳过即可;它后面还有内容
			// 就说明是中间行损坏 —— 真损坏必须报出来,不能当成"又有一次崩溃"。
			if onlyBlank(lines[i+1:]) {
				break
			}
			return nil, fmt.Errorf("fleet: 观测侧车 %s 第 %d 行非法: %w", r.seenPath(), i+1, err)
		}
		seen[e.NodeID] = e.LastSeenUnix
	}
	return seen, nil
}

// onlyBlank 报告剩余行是否全空(判定"末尾半截行"用)。
func onlyBlank(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return false
		}
	}
	return true
}

// foldSeen 把侧车折叠到身份表之上(原地改 LastSeenUnix)。只更新身份表里**已
// 存在**的节点:侧车残留的已删 node_id 不得让节点复活。
func (r *Registry) foldSeen(nodes []Node) ([]Node, error) {
	seen, err := r.loadSeen()
	if err != nil {
		return nil, err
	}
	if len(seen) == 0 {
		return nodes, nil
	}
	for i := range nodes {
		if v, ok := seen[nodes[i].NodeID]; ok {
			nodes[i].LastSeenUnix = v
		}
	}
	return nodes, nil
}

// seenCompactThreshold 返回当前节点数对应的压实阈值。
func (r *Registry) seenCompactThreshold(nodes int) int {
	threshold := nodes * seenCompactFactor
	if threshold < seenCompactFloor {
		threshold = seenCompactFloor
	}
	return threshold
}

// countSeenLines 只数行数(不解析):压实判定在 markSeen 热路径上,没必要为它
// 把整个侧车反序列化一遍。末尾没有换行的半截行也算一行(偏保守,早压无害)。
func (r *Registry) countSeenLines() (int, error) {
	raw, err := os.ReadFile(r.seenPath())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("fleet: 读取观测侧车 %s 失败: %w", r.seenPath(), err)
	}
	n := bytes.Count(raw, []byte("\n"))
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		n++
	}
	return n, nil
}

// compactSeen 在行数越过阈值时把侧车收缩成"每个被观测过的节点一行"。压实自身
// 也必须原子替换(临时文件 → fsync → rename):半截侧车会被读侧当成损坏。
func (r *Registry) compactSeen(nodes []Node) error {
	lines, err := r.countSeenLines()
	if err != nil {
		return err
	}
	if lines <= r.seenCompactThreshold(len(nodes)) {
		return nil
	}
	seen, err := r.loadSeen()
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(r.root, ".seen-*.tmp")
	if err != nil {
		return fmt.Errorf("fleet: 建侧车临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后此处是 no-op

	w := bufio.NewWriter(tmp)
	for i := range nodes {
		v, ok := seen[nodes[i].NodeID]
		if !ok {
			continue
		}
		b, err := json.Marshal(seenEntry{NodeID: nodes[i].NodeID, LastSeenUnix: v})
		if err != nil {
			tmp.Close()
			return fmt.Errorf("fleet: 序列化 last_seen %s 失败: %w", nodes[i].NodeID, err)
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			tmp.Close()
			return fmt.Errorf("fleet: 写观测侧车失败: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return fmt.Errorf("fleet: 刷观测侧车失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fleet: 同步观测侧车失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fleet: 关闭侧车临时文件失败: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("fleet: 设置观测侧车权限失败: %w", err)
	}
	if err := os.Rename(tmpName, r.seenPath()); err != nil {
		return fmt.Errorf("fleet: 原子替换观测侧车失败: %w", err)
	}
	return nil
}

// resetSeen 清空侧车:调用方(Put/Delete)已经把观测折叠进身份表,删文件即可。
func (r *Registry) resetSeen() error {
	if err := os.Remove(r.seenPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("fleet: 清空观测侧车失败: %w", err)
	}
	return nil
}
