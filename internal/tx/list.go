// list.go —— journal 全量枚举,期望视图投影(desiredview)的读取底座。
//
// 排序即投影回放序:created_at 升序、同刻按 id 升序(全序,回放结果确定)。
// 文件名不过 tx-id 形状门的条目(杂散文件)跳过不算错;形状吻合但读不出/
// 解析失败 = journal 损坏,如实报错(fail-closed,绝不静默丢一条期望)。
package tx

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Daedalusys/daedalus-sdk/dirs"
)

// List 按回放顺序枚举 root 下的全部事务(复用 Load 的读侧纪律:
// LOCK_SH、内容 id 与文件名一致性、状态全集校验)。
func List() ([]*Transaction, error) {
	root, err := dirs.TxRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("tx: 枚举日志根失败: %w", err)
	}
	var txs []*Transaction
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if !txIDPattern.MatchString(id) {
			continue // 杂散文件不是 journal,不参与投影
		}
		t, err := Load(id)
		if err != nil {
			return nil, err
		}
		txs = append(txs, t)
	}
	sort.Slice(txs, func(i, j int) bool {
		if !txs[i].CreatedAt.Equal(txs[j].CreatedAt) {
			return txs[i].CreatedAt.Before(txs[j].CreatedAt)
		}
		return txs[i].ID < txs[j].ID
	})
	return txs, nil
}
