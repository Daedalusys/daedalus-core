package main

// organize_move.go —— organize.move 事务适配器(daedalus.organize 插件的写通道)。
//
// 三段生命周期(与 adapter.go 的 Adapter 契约对齐):
//   - Propose: 解析 args → 每个 move 经 pathguard.ValidateWritePath 校验
//     from(source,必须存在)+ to 的最深现存祖先(防止写逃逸出白名单/写
//     只读系统目录);快照 from 现状{mode/size/sha256};零副作用。
//   - Apply:   重校验纵深防御 → MkdirAll(to 的父目录,幂等)→ os.Rename;
//     任一 rename 失败立即返 Returncode=1 并携带失败目标。
//   - Rollback: 倒序 os.Rename(to → from),前提是 from 的父目录仍在白名单内
//     且父目录可建;不尝试恢复已删除的子目录树(mkdir 不撤销,与 issue #3
//     范围对齐)。
//
// 安全约束:
//   - 写二档校验在任何副作用之前, fail-closed;
//   - 直 argv(non-exec), 不 spawn shell;
//   - confirm_token 校验由插件侧完成(在 spawn 本适配器之前), tx 层不感知 token。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Daedalusys/daedalus-core/internal/tx"
	"github.com/Daedalusys/daedalus-sdk/pathguard"
)

// organizeMoveArgs 是步骤 args 的线上形态(v1 单层 schema, 未知键拒绝)。
type organizeMoveArgs struct {
	PlanID string             `json:"plan_id"`
	Moves  []organizeMoveEntry `json:"moves"`
}

// organizeMoveEntry 是单条 move 的入参形态。size_bytes/rule 留作审计上下文,
// 不参与校验与副作用判定。
type organizeMoveEntry struct {
	From      string `json:"from"`
	To        string `json:"to"`
	SizeBytes int64  `json:"size_bytes"`
	Rule      string `json:"rule"`
}

// organizeSnapshot 是单条 move 在 Propose 期的 from 现状快照。sha256 用于
// 漂移检测/审计线索, 不参与 Rollback 的内容恢复(issue #3 范围)。
type organizeSnapshot struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size_bytes"`
	SHA256 string `json:"sha256,omitempty"`
}

// organizeMoveBefore 是 Propose 期快照集合(回滚依据: 知道 from/to, 倒序
// os.Rename(to→from) 即恢复文件名布局)。
type organizeMoveBefore struct {
	Snapshots []organizeSnapshot `json:"snapshots"`
}

// organizeMoveAfter 是提议的目标态视图: 待应用的 move 列表。
type organizeMoveAfter struct {
	Moves []organizeMoveRecord `json:"moves"`
}

// organizeMoveRecord 是 after 的纯形态: 仅 from/to, 不带大小/哈希等冗余。
type organizeMoveRecord struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// organizeMoveAdapter 无状态空壳: 三段生命周期已全部落地, RegisterAdapter 进 registry。
type organizeMoveAdapter struct{}

// parseOrganizeMoveArgs 边界解析(DisallowUnknownFields: 手改注入的多余键即拒)。
func parseOrganizeMoveArgs(raw json.RawMessage) (organizeMoveArgs, error) {
	var in organizeMoveArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("invalid organize.move args: %w", err)
	}
	if dec.More() {
		return in, errors.New("invalid organize.move args: JSON 文档后有尾随数据")
	}
	if len(in.Moves) == 0 {
		return in, errors.New("organize.move requires at least one move")
	}
	for i, m := range in.Moves {
		if m.From == "" || m.To == "" {
			return in, fmt.Errorf("organize.move move[%d] 缺 from/to", i)
		}
		if m.From == m.To {
			return in, fmt.Errorf("organize.move move[%d] from == to %q 无效", i, m.From)
		}
	}
	return in, nil
}

// Propose 校验并快照每个 from 路径, 零副作用(to 在 Apply 期才被创建)。
// to 的校验通过"最深现存祖先 + ValidateWritePath"实现: 允许 to 尚不存在
// (os.Rename 会创建), 但父目录必须真实存在且落在白名单内。
func (organizeMoveAdapter) Propose(_ context.Context, args json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	in, err := parseOrganizeMoveArgs(args)
	if err != nil {
		return nil, nil, err
	}

	before := organizeMoveBefore{Snapshots: make([]organizeSnapshot, 0, len(in.Moves))}
	after := organizeMoveAfter{Moves: make([]organizeMoveRecord, 0, len(in.Moves))}
	for i, m := range in.Moves {
		fromSafe, err := pathguard.ValidateWritePath(m.From)
		if err != nil {
			return nil, nil, fmt.Errorf("organize.move move[%d] from 写二档拒绝 %q: %w", i, m.From, err)
		}
		canonicalTo, err := canonicalizeDest(m.To)
		if err != nil {
			return nil, nil, fmt.Errorf("organize.move move[%d] to 写二档拒绝 %q: %w", i, m.To, err)
		}
		// 纵深防御: canonicalTo 若真实已存在, 则冲突未被前置 strategy 处理, 拒绝
		// (策略已经 rename/skip/abort 处理过, 此处命中即视为漂移)。
		if _, err := os.Lstat(canonicalTo); err == nil {
			return nil, nil, fmt.Errorf("organize.move move[%d] to %q 已存在, 冲突未被前置 strategy 处理", i, canonicalTo)
		}
		info, err := os.Lstat(fromSafe)
		if err != nil {
			return nil, nil, fmt.Errorf("organize.move move[%d] from 不可读 %q: %w", i, fromSafe, err)
		}
		snap := organizeSnapshot{
			From: fromSafe,
			To:   canonicalTo,
			Mode: uint32(info.Mode().Perm()),
			Size: info.Size(),
		}
		if info.Mode().IsRegular() {
			sum, err := organizeSha256File(fromSafe)
			if err != nil {
				return nil, nil, fmt.Errorf("organize.move move[%d] 计算 sha256 失败 %q: %w", i, fromSafe, err)
			}
			snap.SHA256 = sum
		}
		before.Snapshots = append(before.Snapshots, snap)
		after.Moves = append(after.Moves, organizeMoveRecord{From: fromSafe, To: m.To})
	}

	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return nil, nil, fmt.Errorf("organize.move 序列化 before 失败: %w", err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return nil, nil, fmt.Errorf("organize.move 序列化 after 失败: %w", err)
	}
	return beforeJSON, afterJSON, nil
}

// canonicalizeDest 把 to 解析成"通过真实祖先 EvalSymlinks 后接词法尾段"的
// 绝对路径。目的:封堵 `link/../etc/x` 这种借中间符号链接逃出白名单的写法。
// 词法层面 filepath.Dir(to) 已经把 .. 折叠,但这只对纯词法路径成立;若中间
// 某段是符号链接,内核解释时 .. 会按链接目标的父目录走,词法净化与内核解析
// 不再等价。这里把最深现存祖先用 EvalSymlinks 固化,再用 filepath.Rel + Join
// 把剩余尾段拼回,确保内核看到的目标路径的"父目录"与校验通过的父目录同源。
// 同时显式拒绝相对路径、空字节、尾段含 .. 段,以多层防线覆盖边角。
func canonicalizeDest(to string) (string, error) {
	if to == "" {
		return "", fmt.Errorf("dest must be non-empty")
	}
	if strings.ContainsRune(to, 0) {
		return "", fmt.Errorf("dest contains null byte")
	}
	if !strings.HasPrefix(to, "/") {
		return "", fmt.Errorf("dest must be absolute path: %q", to)
	}
	p := to
	for {
		parent := filepath.Dir(p)
		if parent == p {
			// 已到根,根必然存在。但 root 通常不在 AllowedDirs,直接拒。
			_, err := pathguard.ValidateWritePath(parent)
			return "", err
		}
		if _, err := os.Lstat(parent); err == nil {
			resolved, err := filepath.EvalSymlinks(parent)
			if err != nil {
				return "", fmt.Errorf("dest 父目录 %q 无法解析: %w", parent, err)
			}
			rel, err := filepath.Rel(parent, to)
			if err != nil {
				return "", fmt.Errorf("dest 尾段计算失败: %w", err)
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("dest %q 含 .. 段, 拒绝", to)
			}
			canonical := filepath.Join(resolved, rel)
			if _, err := pathguard.ValidateWritePath(resolved); err != nil {
				return "", err
			}
			return canonical, nil
		}
		p = parent
	}
}

// Apply 重校验 + MkdirAll 父目录 + os.Rename。
func (organizeMoveAdapter) Apply(_ context.Context, step tx.Step) tx.OpResult {
	var before organizeMoveBefore
	if err := json.Unmarshal(step.BeforeState, &before); err != nil {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("organize.move 解析 before 失败: %v", err)}
	}
	for _, snap := range before.Snapshots {
		// 纵深防御: Apply 再次校验 from(防 BeforeState 被篡改)。
		if _, err := pathguard.ValidateWritePath(snap.From); err != nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("organize.move Apply 写二档拒绝 from %q: %v", snap.From, err)}
		}
		// 重新规范化 to(防 BeforeState.To 被篡改或中间链被替换)。
		canonicalTo, err := canonicalizeDest(snap.To)
		if err != nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("organize.move Apply 写二档拒绝 to %q: %v", snap.To, err)}
		}
		if err := os.MkdirAll(filepath.Dir(canonicalTo), 0755); err != nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("organize.move Apply 创建 to 父目录 %q 失败: %v", filepath.Dir(canonicalTo), err)}
		}
		if err := os.Rename(snap.From, canonicalTo); err != nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("organize.move Apply rename %q → %q 失败: %v", snap.From, canonicalTo, err)}
		}
	}
	return tx.OpResult{Returncode: 0}
}

// Rollback 倒序 os.Rename(to → from), 重建原始文件名布局;from 的父目录已
// 存在于 Propose 期(目标文件已存在意味着父目录必然存在), 此处仅做
// MkdirAll 幂等保险。
func (organizeMoveAdapter) Rollback(_ context.Context, step tx.Step) tx.OpResult {
	var before organizeMoveBefore
	if err := json.Unmarshal(step.BeforeState, &before); err != nil {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("organize.move 解析 before 失败: %v", err)}
	}
	for i := len(before.Snapshots) - 1; i >= 0; i-- {
		snap := before.Snapshots[i]
		// from 在 Apply 后已不存在, 规范化其最深现存祖先并校验。
		canonicalFrom, err := canonicalizeDest(snap.From)
		if err != nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("organize.move Rollback 写二档拒绝 from %q: %v", snap.From, err)}
		}
		if err := os.MkdirAll(filepath.Dir(canonicalFrom), 0755); err != nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("organize.move Rollback 创建 from 父目录 %q 失败: %v", filepath.Dir(canonicalFrom), err)}
		}
		// canonicalFrom 若已存在(极端并发/手动干预), 失败即停, 不强行覆盖。
		if _, err := os.Lstat(canonicalFrom); err == nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("organize.move Rollback from %q 已存在, 无法还原", canonicalFrom)}
		}
		if err := os.Rename(snap.To, canonicalFrom); err != nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("organize.move Rollback rename %q → %q 失败: %v", snap.To, canonicalFrom, err)}
		}
	}
	return tx.OpResult{Returncode: 0}
}

// organizeSha256File 流式计算文件摘要(用于漂移检测/审计线索,非内容备份)。
func organizeSha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
