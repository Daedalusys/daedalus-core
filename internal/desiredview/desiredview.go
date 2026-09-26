// Package desiredview 把 tx journal 投影为 Current Desired View(期望视图)。
//
// 语义单一事实源见 docs/desired-state-projection.md(daedalus-sdk#4 裁决):
//   - 回放序 = tx.List() 的 created_at/id 全序,后写覆盖前写;
//   - 唯一提交点是 applied:proposed/applying/failed/rolled_back 不贡献期望;
//   - adapter→kind 是封闭映射表,表外忽略、表内解析失败即整体报错(fail-closed);
//   - 视图是派生缓存,可删可重建,重建 = 全量 replay(无增量水位线)。
//
// 消费者是未来的 daedalus-controller(VISION §10 P4);本包零副作用、
// 不写任何文件、不驱动任何变更。
package desiredview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

// adapterKinds 是适配器 → 资源 kind 的封闭映射(与 cmd/daedalus-tx 注册表同步;
// 新 transactable kind 落地时在此登记,同时更新投影文档)。
var adapterKinds = map[string]objectmodel.Kind{
	"service.set": objectmodel.KindService,
	"package.set": objectmodel.KindPackage,
}

// stepArgs 是已知适配器 args 的投影侧最小形状(两键;未知键拒绝,
// 与各适配器 parse*Args 的注入防线一致)。DesiredState 用指针区分
// 「缺失」与「空串」——键缺失即损坏,不得投影为空期望。
type stepArgs struct {
	Name         string  `json:"name"`
	DesiredState *string `json:"desired_state"`
}

// Entry 是期望视图一行:某资源当前期望 + 贡献该期望的最后事务 id。
// SourceTx 是 generation 语义的第一个真实消费点(P4 B3 前置)。
type Entry struct {
	Kind         objectmodel.Kind `json:"kind"`
	Name         string           `json:"name"`
	DesiredState string           `json:"desired_state"`
	SourceTx     string           `json:"source_tx"`
}

type key struct{ kind, name string }

// View 是 Current Desired View:派生缓存,只读投影结果。
type View struct { m map[key]Entry }

// Get 返回指定资源当前期望;ok=false 表示无任何 applied 事务声明过它。
func (v *View) Get(kind objectmodel.Kind, name string) (Entry, bool) {
	e, ok := v.m[key{string(kind), name}]
	return e, ok
}

// Entries 按 (kind, name) 升序返回全部行(确定性输出,供事件/diff 消费)。
func (v *View) Entries() []Entry {
	out := make([]Entry, 0, len(v.m))
	for _, e := range v.m {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Project 是把事务列表(须已按回放序排列)投影为视图的纯函数。
// 表外 adapter 忽略(非对象模型资源);表内 args 解析失败 → 整体报错,
// 绝不静默丢弃一条期望。
func Project(txs []*tx.Transaction) (*View, error) {
	v := &View{m: map[key]Entry{}}
	for _, t := range txs {
		if t == nil || t.Status != tx.StatusApplied {
			continue
		}
		for i, s := range t.Steps {
			kind, ok := adapterKinds[s.Adapter]
			if !ok {
				continue
			}
			a, err := parseArgs(s.Args)
			if err != nil {
				return nil, fmt.Errorf("desiredview: 事务 %s 第 %d 步(%s)args 损坏: %w", t.ID, i, s.Adapter, err)
			}
			v.m[key{string(kind), a.Name}] = Entry{
				Kind: kind, Name: a.Name, DesiredState: *a.DesiredState, SourceTx: t.ID,
			}
		}
	}
	return v, nil
}

// Load 从 dirs.TxRoot() 全量重放(journal 损坏即报错;上界 = 事务文件数)。
func Load() (*View, error) {
	txs, err := tx.List()
	if err != nil {
		return nil, err
	}
	return Project(txs)
}

func parseArgs(raw json.RawMessage) (stepArgs, error) {
	if len(raw) == 0 {
		return stepArgs{}, errors.New("args 为空")
	}
	var a stepArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return stepArgs{}, err
	}
	if a.Name == "" {
		return stepArgs{}, errors.New("name 为空")
	}
	if a.DesiredState == nil {
		return stepArgs{}, errors.New("desired_state 缺失")
	}
	return a, nil
}
