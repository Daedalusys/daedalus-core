package main

// stamp.go —— 事务审计盖章 + 跨进程事务链续接(todo 15 承重)。
//
// 盖章规则(review round 1, 与 todo 23(d) 消歧后钉死):
//   - 只有 begin(step 0)/ apply(步 1..N 升序)/ rollback(步 N+1..)携带 Entry.TxID/TxStep;
//   - propose 与 status 发出**空 TxID**条目(tx-id 仅存在于 args 对象里)。
//   - identity=daedalus-tx, tool=daedalus_tx_<sub>; 尽力而为发射(写失败静默,
//     绝不拖垮子命令本体), 镜像 daedalus-host/main.go 的 hostAudit。
//
// 跨进程续链: begin/apply/rollback 是**不同进程**的调用, 事务内步的 tx_prev_hash 必须
// 指向"本事务上一条 in-tx 记录"的 entry_hash。本 CLI 在 apply/rollback 开始时**扫描一次**
// 审计日志尾部取该事务的最大 tx_step 与对应 entry_hash(prevHash), 之后进程内多步用
// LogAudit 返回的 rec.EntryHash 本地串接(不重复扫描)。begin 的创世 tx_prev_hash 则交给
// internal/audit 的 flock 内播种(LogAudit: TxID!=""&&TxPrevHash=="" → lastNonTxRecord)。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/daedalus-os/daedalus/core/internal/audit"
)

// outcomeOf 把退出码映射为审计 outcome(success/error/denied), 与 host 一致。
func outcomeOf(code int) string {
	switch code {
	case exitRuntime:
		return "error"
	case exitUsage:
		return "denied"
	default:
		return "success"
	}
}

// argsVal 把 map 序列化为审计 args 的 *audit.Value; 编码失败兜底原始字符串(不应发生)。
func argsVal(m map[string]any) *audit.Value {
	data, err := json.Marshal(m)
	if err != nil {
		return audit.NewString(fmt.Sprint(m))
	}
	v, err := audit.ParseValue(string(data))
	if err != nil {
		return audit.NewString(string(data))
	}
	return v
}

// stampPlain 发一条**空 TxID**的事务外围审计(propose/status): tx-id 只在 args 里。
// best-effort: 任何写失败静默忽略(返回 "" 表示未落盘, 供调用方忽略即可)。
func stampPlain(sub, id, outcome string) {
	_, _ = audit.LogAudit(audit.Entry{ //nolint:errcheck // 尽力而为, 与 hostAudit 同纪律
		Identity: txIdentity,
		Tool:     "daedalus_tx_" + sub,
		Args:     argsVal(map[string]any{"tx_id": id}),
		Outcome:  outcome,
	})
}

// stampTx 发一条**盖章**事务条目(begin/apply/rollback): 携带 TxID/TxStep/TxPrevHash。
// prevHash 为空且 step==0(begin)时由 LogAudit 播种创世; apply/rollback 传入显式 prevHash。
// 返回落盘记录的 entry_hash(供下一步本地串链); 写失败返回 ""(链在此断开, 后续步降级
// 为创世播种 —— 与"审计尽力而为"一致, 主副作用仍照常施加)。
func stampTx(sub, id string, step int, prevHash, outcome string, extra map[string]any) string {
	m := map[string]any{"tx_id": id}
	for k, v := range extra {
		m[k] = v
	}
	rec, err := audit.LogAudit(audit.Entry{
		Identity:   txIdentity,
		Tool:       "daedalus_tx_" + sub,
		Args:       argsVal(m),
		Outcome:    outcome,
		TxID:       id,
		TxStep:     step,
		TxPrevHash: prevHash,
	})
	if err != nil || rec == nil {
		return ""
	}
	return rec.EntryHash
}

// txTail 是本事务审计链尾: 上一条 in-tx 记录的 entry_hash 与其 tx_step。
type txTail struct {
	prevHash string
	maxStep  int
}

// scanTxTail 扫描审计日志(默认路径), 返回该 tx-id 已盖章记录的**最大 step** 及其 entry_hash。
// 无匹配(或日志不存在)→ 返回零值 txTail(prevHash 空, maxStep 0), 调用方据此把下一条
// 当作 step-1 起算且创世留空(由 LogAudit 播种)。逐行前向扫, 取 step 最大者(追加序=升序,
// 等价"最后一条"), 不依赖 tx_step 一定连续(失败步也可能已盖章)。
func scanTxTail(txID string) txTail {
	path := audit.DefaultLogPath()
	f, err := os.Open(path)
	if err != nil {
		return txTail{}
	}
	defer f.Close()
	tail := txTail{maxStep: 0}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		v, perr := audit.ParseValue(string(line))
		if perr != nil || !v.IsObject() {
			continue
		}
		if tid, ok := v.LookupString("tx_id"); !ok || tid != txID {
			continue
		}
		eh, ok := v.LookupString("entry_hash")
		if !ok {
			continue
		}
		step := 0
		if sv, ok := v.Lookup("tx_step"); ok && sv != nil {
			if n, e := strconv.Atoi(sv.ArgsString()); e == nil {
				step = n
			}
		}
		if step >= tail.maxStep {
			tail.maxStep, tail.prevHash = step, eh
		}
	}
	return tail
}
