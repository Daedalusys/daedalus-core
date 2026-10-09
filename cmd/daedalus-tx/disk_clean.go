package main

// disk_clean.go —— disk.clean 事务适配器(daedalus.diskclean 插件的写通道)。
//
// 三段生命周期(与 adapter.go 的 Adapter 契约对齐):
//   - Propose: 解析 {paths []string} → 每个 path 经 pathguard.ValidateWritePath
//     二档校验(拒写只读系统目录 + 敏感文件 + 强制完整 realpath)→ 快照每个
//     目标的 {path, mode, size, sha256}(只 stat + 读内容算 sha256,零副作用)。
//   - Apply:   重校验(纵深防御)→ 对每个 path os.Remove(文件)或 os.RemoveAll
//     (目录,盘面清理场景少见但不排除); 返回 OpResult。
//   - Rollback: 依据 step.BeforeState 重建 inode(touch + 恢复 mode);
//     内容不恢复(issue #1 范围排除"内容完整恢复"); 重建失败返 OpResult.Returncode=1。
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

	"github.com/Daedalusys/daedalus-core/internal/tx"
	"github.com/Daedalusys/daedalus-sdk/pathguard"
)

// diskCleanArgs 是步骤 args 的线上形态(v1 单键 schema, 未知键拒绝)。
type diskCleanArgs struct {
	Paths []string `json:"paths"`
}

// diskSnapshot 是单条目标在 Propose 期的现状快照。内容不保存(SHA-256 用于
// 漂移检测, 不用于内容恢复)。IsDir 记录类型位(目录 vs 常规文件)以让
// Rollback 重建正确的 inode 类型 — 只记 perm 位会把目录退化为空文件。
type diskSnapshot struct {
	Path   string `json:"path"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	IsDir  bool   `json:"is_dir,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// diskCleanBefore 是 Propose 期的现状集合(回滚依据)。
type diskCleanBefore struct {
	Snapshots []diskSnapshot `json:"snapshots"`
}

// diskCleanAfter 提议的目标态视图:被删除的路径列表。
type diskCleanAfter struct {
	Deleted []string `json:"deleted"`
}

// diskCleanAdapter 无状态空壳: 生命周期三段已全部落地; RegisterAdapter 进 registry。
type diskCleanAdapter struct{}

// parseDiskCleanArgs 边界解析(DisallowUnknownFields: 手改注入的多余键即拒)。
func parseDiskCleanArgs(raw json.RawMessage) (diskCleanArgs, error) {
	var in diskCleanArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("invalid disk.clean args: %w", err)
	}
	if dec.More() {
		return in, errors.New("invalid disk.clean args: JSON 文档后有尾随数据")
	}
	if len(in.Paths) == 0 {
		return in, errors.New("disk.clean requires at least one path")
	}
	return in, nil
}

// Propose 校验并快照目标路径, 零副作用。
func (diskCleanAdapter) Propose(_ context.Context, args json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	in, err := parseDiskCleanArgs(args)
	if err != nil {
		return nil, nil, err
	}

	before := diskCleanBefore{Snapshots: make([]diskSnapshot, 0, len(in.Paths))}
	after := diskCleanAfter{Deleted: make([]string, 0, len(in.Paths))}
	for _, p := range in.Paths {
		safe, err := pathguard.ValidateWritePath(p)
		if err != nil {
			return nil, nil, fmt.Errorf("disk.clean 写二档拒绝 %q: %w", p, err)
		}
		info, err := os.Lstat(safe)
		if err != nil {
			return nil, nil, fmt.Errorf("disk.clean 目标不可读 %q: %w", safe, err)
		}
		snap := diskSnapshot{
			Path:  safe,
			Mode:  uint32(info.Mode().Perm()),
			Size:  info.Size(),
			IsDir: info.IsDir(),
		}
		if info.Mode().IsRegular() {
			sum, err := sha256File(safe)
			if err != nil {
				return nil, nil, fmt.Errorf("disk.clean 计算 sha256 失败 %q: %w", safe, err)
			}
			snap.SHA256 = sum
		}
		before.Snapshots = append(before.Snapshots, snap)
		after.Deleted = append(after.Deleted, safe)
	}

	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return nil, nil, fmt.Errorf("disk.clean 序列化 before 失败: %w", err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return nil, nil, fmt.Errorf("disk.clean 序列化 after 失败: %w", err)
	}
	return beforeJSON, afterJSON, nil
}

// Apply 删除目标(文件 os.Remove;目录 os.RemoveAll)。重校验写二档。
func (diskCleanAdapter) Apply(_ context.Context, step tx.Step) tx.OpResult {
	var before diskCleanBefore
	if err := json.Unmarshal(step.BeforeState, &before); err != nil {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("disk.clean 解析 before 失败: %v", err)}
	}
	for _, snap := range before.Snapshots {
		// 纵深防御:Apply 再次校验(防 BeforeState 被篡改)。
		if _, err := pathguard.ValidateWritePath(snap.Path); err != nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("disk.clean Apply 写二档拒绝 %q: %v", snap.Path, err)}
		}
		if err := os.RemoveAll(snap.Path); err != nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("disk.clean 删除 %q 失败: %v", snap.Path, err)}
		}
	}
	return tx.OpResult{Returncode: 0}
}

// Rollback 重建 inode(目录 MkdirAll / 文件 OpenFile)并恢复 mode。内容不恢复
//(见文件头约束)。IsDir 为 true 时重建为目录,否则重建为空文件。
func (diskCleanAdapter) Rollback(_ context.Context, step tx.Step) tx.OpResult {
	var before diskCleanBefore
	if err := json.Unmarshal(step.BeforeState, &before); err != nil {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("disk.clean 解析 before 失败: %v", err)}
	}
	for _, snap := range before.Snapshots {
		if snap.IsDir {
			if err := os.MkdirAll(snap.Path, os.FileMode(snap.Mode)); err != nil {
				return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("disk.clean 回滚重建目录 %q 失败: %v", snap.Path, err)}
			}
			if err := os.Chmod(snap.Path, os.FileMode(snap.Mode)); err != nil {
				return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("disk.clean 回滚恢复 mode %q 失败: %v", snap.Path, err)}
			}
			continue
		}
		f, err := os.OpenFile(snap.Path, os.O_CREATE|os.O_WRONLY, os.FileMode(snap.Mode))
		if err != nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("disk.clean 回滚重建 %q 失败: %v", snap.Path, err)}
		}
		_ = f.Close()
		if err := os.Chmod(snap.Path, os.FileMode(snap.Mode)); err != nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("disk.clean 回滚恢复 mode %q 失败: %v", snap.Path, err)}
		}
	}
	return tx.OpResult{Returncode: 0}
}

// sha256File 流式计算文件摘要(用于快照漂移检测,非内容备份)。
func sha256File(path string) (string, error) {
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
