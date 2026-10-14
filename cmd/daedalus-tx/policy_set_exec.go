package main

// policy_set_exec.go —— policy.set 的文件 I/O 与路径解析助手层。
//
// policy_set.go 负责三段生命周期编排;本文件做 policyFilePath 注入、原子写
// (防"写了没生效")、ref 路径解析、sha256 校验四件机械事。SDK 跨仓的
// policy.DisabledSentinel 是 sentinel 字节唯一事实源(避免跨仓字面量漂移)。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Daedalusys/daedalus-sdk/policy"
)

// policyFilePath 是 policy.set 写入 / 读取的目标路径。生产 = `/opt/daedalus/shared/policy.toml`,
// 测试通过 t.Cleanup 覆写与还原(模式镜像 systemctlUserBinary / podmanExecutable)。
// 注意:不是 const,因为要支持测试注入;但默认值等同于 SDK 的 policy.ProductionPath.
var policyFilePath = policy.ProductionPath

// policyScopeRejectMsg 三段共用 euid!=0 拒串(逐字锁定,测试与 AGENTS.md 描述引用)。
const policyScopeRejectMsg = "policy.set requires root (euid=0) to write /opt/daedalus/shared/policy.toml"

// readPolicyFileBytes 读整份文件;不存在 → (nil, nil)(不当作错,Propose 期仅用于 loaded;
// absent 不关心内容)。其余读错如实上抛。
func readPolicyFileBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	return data, nil
}

// writePolicyFileAtomic 写入 + 读回 sha256 一致性校验两步:
//  1. 写 `<path>.tmp.<rand>` → Sync → Rename → 父目录 Sync(同 fs 内原子).
//  2. 读回,计算 sha256,必须 == sha256Hex(content).
//     任一步失败返 error,调用方据此判 OpResult.Returncode=1.
func writePolicyFileAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		// 失败路径清理残留 tmp(成功路径 Rename 后路径已变化,清理无副作用)
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("临时文件 fsync 失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename 失败: %w", err)
	}
	// 父目录 fsync(同 fs 内 rename 持久化要求;ext4/xfs 必备)
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	// 读回校验 sha256(防"写了没生效")
	got, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读回校验失败: %w", err)
	}
	if sha256Hex(got) != sha256Hex(content) {
		return fmt.Errorf("write verify failed: sha256 mismatch (expected %s, got %s)",
			sha256Hex(content), sha256Hex(got))
	}
	return nil
}

// policyPathResolve 解析 ref(相对 baseDir)→ cleaned 绝对路径;**拒绝**:
//   - 绝对路径(以 / 开头)
//   - 含 `..` 回溯段
//   - 解析后落在 baseDir 之外
//
// 不解析符号链接(留给 v2;当前 v1 assume ref 路径在受信任 manifest 同仓目录)。
func policyPathResolve(ref, baseDir string) (string, error) {
	if filepath.IsAbs(ref) {
		return "", fmt.Errorf("content_ref must be a relative path, got absolute %q", ref)
	}
	if strings.Contains(ref, "..") {
		return "", fmt.Errorf("content_ref must not contain '..': %q", ref)
	}
	base, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("解析 base dir 失败: %w", err)
	}
	abs := filepath.Clean(filepath.Join(base, ref))
	rel, err := filepath.Rel(base, abs)
	if err != nil {
		return "", fmt.Errorf("ref 路径解析失败: %w", err)
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("content_ref escapes base dir: %q", ref)
	}
	return abs, nil
}

// sha256Hex 计算 sha256 hex 摘要(小写,前后无 0x 前缀).
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// detectSentinel 严格首行匹配 SDK DisabledSentinel(防"包装过的哨兵"绕过).
func detectSentinel(data []byte) bool {
	return bytes.HasPrefix(data, []byte(policy.DisabledSentinel))
}
