package patrol_test

// 本文件钉住变化驱动的邮件口径:主题带节点与三个计数,正文三组都贴工具原文证据,
// 没有三类变化(含只有持续项)时一封信都不发。

import (
	"errors"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/patrol"
)

// mailFixture 返回一份三类各一条、另带一条持续项的变化集。
// 证据故意包含换行与中文,用来钉住「逐字贴、不转义、不摘要」。
func mailFixture() patrol.Changes {
	return patrol.Changes{
		Added: []patrol.Finding{{
			Node:     "lab-a",
			ItemID:   "disk-health",
			Status:   patrol.StatusProblem,
			Severity: patrol.SeverityWarning,
			Summary:  "/tmp 使用率 92%",
			Evidence: []patrol.Evidence{{
				Tool:  "daedalus.shell/shell_exec",
				Input: `{"argv":["df","-h","/tmp"]}`,
				Output: "Filesystem      Size  Used Avail Use% Mounted on\n" +
					"/dev/vdb1       40G   37G  3.0G  92% /tmp\n" +
					`<script>alert("x&y")</script>`,
				ReturnCode: 0,
			}},
		}},
		Worsened: []patrol.Finding{{
			Node:     "lab-a",
			ItemID:   "service-health",
			Status:   patrol.StatusProblem,
			Severity: patrol.SeverityCritical,
			Summary:  "单元 kvm-storage.service 由 failed 变为崩溃循环",
			Evidence: []patrol.Evidence{{
				Tool:       "daedalus.service/service.query",
				Input:      `{"unit":"kvm-storage.service"}`,
				Output:     "ActiveState=failed\nSubState=failed\nNRestarts=17",
				ReturnCode: 0,
			}},
		}},
		Recovered: []patrol.Finding{{
			Node:     "lab-a",
			ItemID:   "process-health",
			Status:   patrol.StatusProblem,
			Severity: patrol.SeverityWarning,
			Summary:  "18080 端口多余监听已消失",
			Evidence: []patrol.Evidence{{
				Tool:       "daedalus.proc/proc_list",
				Input:      `{"port":18080}`,
				Output:     "no listener on :18080",
				ReturnCode: 0,
			}},
		}},
		Ongoing: []patrol.Finding{{
			Node:     "lab-a",
			ItemID:   "log-health",
			Status:   patrol.StatusProblem,
			Severity: patrol.SeverityInfo,
			Summary:  "持续项不该进邮件正文",
		}},
	}
}

func TestFormatMail_ThreeGroups(t *testing.T) {
	ch := mailFixture()
	subject, body := patrol.FormatMail("lab-a", ch)

	for _, want := range []string{"lab-a", "新增 1", "恶化 1", "恢复 1"} {
		if !strings.Contains(subject, want) {
			t.Errorf("主题 %q 缺少 %q", subject, want)
		}
	}

	// 组标题与组顺序都钉住:邮件是给人按固定顺序扫的。
	headings := []string{"== 新增 (1) ==", "== 恶化 (1) ==", "== 恢复 (1) =="}
	lastIdx := -1
	for _, heading := range headings {
		idx := strings.Index(body, heading)
		if idx < 0 {
			t.Fatalf("正文缺组标题 %q\n实际正文:\n%s", heading, body)
		}
		if idx < lastIdx {
			t.Errorf("组标题 %q 顺序不对,实际正文:\n%s", heading, body)
		}
		lastIdx = idx
	}

	groups := map[string][]patrol.Finding{
		"新增": ch.Added,
		"恶化": ch.Worsened,
		"恢复": ch.Recovered,
	}
	for name, fs := range groups {
		for _, f := range fs {
			for _, want := range []string{f.ItemID, f.Severity, f.Summary} {
				if !strings.Contains(body, want) {
					t.Errorf("%s 组正文缺少 %q", name, want)
				}
			}
			for _, ev := range f.Evidence {
				// 证据原文必须逐字出现(含换行),否则「证据是工具返回原文」这条信任前提就断了。
				if !strings.Contains(body, ev.Output) {
					t.Errorf("%s 组正文没有原样贴出工具 %s 的输出,期望包含 %q\n实际正文:\n%s",
						name, ev.Tool, ev.Output, body)
				}
			}
		}
	}

	if strings.Contains(body, "持续项不该进邮件正文") {
		t.Errorf("持续项不该出现在正文里,实际正文:\n%s", body)
	}
}

func TestFormatMail_NoChangesIsEmpty(t *testing.T) {
	subject, body := patrol.FormatMail("lab-a", patrol.Changes{})
	if subject != "" || body != "" {
		t.Errorf("没有变化时应返回两个空串,实际 subject=%q body=%q", subject, body)
	}
}

// 只有持续项时也算「没有变化」:一封信都不该发。
func TestFormatMail_OngoingOnlyIsEmpty(t *testing.T) {
	ch := patrol.Changes{Ongoing: mailFixture().Ongoing}
	subject, body := patrol.FormatMail("lab-a", ch)
	if subject != "" || body != "" {
		t.Errorf("只有持续项时应返回两个空串,实际 subject=%q body=%q", subject, body)
	}
}

func TestNotify_NoChangesSendsNothing(t *testing.T) {
	sent := 0
	err := patrol.Notify(patrol.Changes{}, func(subject, body string) error {
		sent++
		return nil
	})
	if err != nil {
		t.Fatalf("无变化时不该有错误,实际 %v", err)
	}
	if sent != 0 {
		t.Errorf("无变化时不该发信,实际发了 %d 封", sent)
	}
}

func TestNotify_OngoingOnlySendsNothing(t *testing.T) {
	sent := 0
	err := patrol.Notify(patrol.Changes{Ongoing: mailFixture().Ongoing}, func(subject, body string) error {
		sent++
		return nil
	})
	if err != nil {
		t.Fatalf("只有持续项时不该有错误,实际 %v", err)
	}
	if sent != 0 {
		t.Errorf("只有持续项时不该发信,实际发了 %d 封", sent)
	}
}

func TestNotify_ChangesSendsOnce(t *testing.T) {
	ch := mailFixture()
	var got []string
	err := patrol.Notify(ch, func(subject, body string) error {
		got = append(got, subject+"\n"+body)
		return nil
	})
	if err != nil {
		t.Fatalf("发信失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("有变化时应恰好发一封,实际 %d 封", len(got))
	}
	// 节点从发现里取:一轮变化都属于同一台机器,调用方不必另传。
	if !strings.Contains(got[0], "lab-a") {
		t.Errorf("主题应带节点 lab-a,实际:\n%s", got[0])
	}
	if !strings.Contains(got[0], "新增 1") {
		t.Errorf("主题应带计数,实际:\n%s", got[0])
	}
}

func TestNotify_PropagatesSendError(t *testing.T) {
	wantErr := errors.New("smtp 投递失败")
	err := patrol.Notify(patrol.Changes{Added: mailFixture().Added}, func(subject, body string) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("投递错误应原样返回,实际 %v", err)
	}
}
