package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRescue_NotOnHeadlessVariant: 在非 headless 镜像上跑 rescue 应非零退出 +
// stderr 提到 "headless"。CI / 开发机 / 大多数主机都不带 VARIANT=headless 标记,
// 这一断言保证救援子命令被限定在 daedalus-atomic headless 变体内。
//
// 这里调 cmdRescue 时传 ["status"] 子子命令:空 args 会被 usage 拦截(stderr
// 不含 "headless"),非空 args 才能走到 IsHeadless 闸门,stderr 报
// "rescue: only on daedalus-atomic headless variant" → 满足断言。
func TestRescue_NotOnHeadlessVariant(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdRescue(&stdout, &stderr, []string{"status"})
	if code == 0 {
		t.Fatalf("expected non-zero on non-headless host, got 0; stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "headless") {
		t.Fatalf("expected stderr to mention headless; got %s", stderr.String())
	}
}

// TestRescue_Status_Structure: 文档化 status 输出所需的最小数据结构契约。
// detectRescueState 必须至少能取到 XrdpUnitState 字符串 — 即便在非 headless
// 主机(systemctl 不可达 / xrdp 未装)也要返回非空("unknown" 兜底),这是
// status 输出格式的稳定前提。
func TestRescue_Status_Structure(t *testing.T) {
	s := detectRescueState()
	if s.XrdpUnitState == "" {
		t.Fatalf("expected XrdpUnitState non-empty, got %+v", s)
	}
}
