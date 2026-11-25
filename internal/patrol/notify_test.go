package patrol_test

// 本文件钉住「只有变化才发信」这条硬要求,以及邮件正文对证据原文的逐字保真:
// 证据被摘要、被缩进或被打断,值班人就失去了回查工具输出的最后一条线索。

import (
	"errors"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/patrol"
)

// threeGroupChanges 是三条各不相同的证据形状:多行原文、单行原文、无证据(恢复项通常没有本轮证据)。
func threeGroupChanges() patrol.Changes {
	return patrol.Changes{
		Added: []patrol.Finding{{
			Node: "lab-a", ItemID: "disk-health", Status: patrol.StatusProblem,
			Severity: patrol.SeverityWarning,
			Summary:  "/dev/vda1 已用 97%,随时写满",
			Evidence: []patrol.Evidence{{
				Tool: "shell_exec", Input: `{"command":"df","args":["-h"]}`,
				Output: "/dev/vda1 40G 40G 0 100% /\n", ReturnCode: 0,
			}},
		}},
		Worsened: []patrol.Finding{{
			Node: "lab-a", ItemID: "kvm-storage", Status: patrol.StatusProblem,
			Severity: patrol.SeverityCritical,
			Summary:  "阵列降级",
			Evidence: []patrol.Evidence{{
				Tool:   "shell_exec",
				Output: "I/O error, dev vdb, sector 2048 op 0x0\nmd/raid1:md0: 3/5 members offline\n",
			}},
		}},
		Recovered: []patrol.Finding{{
			Node: "lab-a", ItemID: "chronyd-down", Status: patrol.StatusProblem,
			Severity: patrol.SeverityWarning, Summary: "chronyd 未运行",
		}},
	}
}

func TestFormatMail_ThreeGroups(t *testing.T) {
	subject, body := patrol.FormatMail("lab-a", threeGroupChanges())

	wantSubject := "[Daedalus] lab-a 巡检：新增 1 · 恶化 1 · 恢复 1"
	if subject != wantSubject {
		t.Fatalf("主题 = %q, 期望 %q", subject, wantSubject)
	}

	for _, want := range []string{"新增", "恶化", "恢复"} {
		if !strings.Contains(body, want) {
			t.Errorf("正文缺少分组标题 %q:\n%s", want, body)
		}
	}

	// 证据原文逐字出现:多行原文也必须整段是正文的子串,不能被缩进或改写打断。
	for _, ev := range [][]patrol.Evidence{
		threeGroupChanges().Added[0].Evidence,
		threeGroupChanges().Worsened[0].Evidence,
	} {
		for _, e := range ev {
			if !strings.Contains(body, e.Output) {
				t.Errorf("证据原文没有逐字出现在正文里: %q\n正文:\n%s", e.Output, body)
			}
			if !strings.Contains(body, e.Tool) {
				t.Errorf("正文没有标出证据来自哪个工具 %q:\n%s", e.Tool, body)
			}
		}
	}

	// 每条发现的身份与结论都要落在邮件里,否则收信人无法回查是哪个清单项。
	for _, want := range []string{
		"disk-health", "/dev/vda1 已用 97%,随时写满", patrol.SeverityWarning,
		"kvm-storage", "阵列降级", patrol.SeverityCritical,
		"chronyd-down", "chronyd 未运行",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("正文缺少 %q:\n%s", want, body)
		}
	}
}

func TestFormatMail_NoChanges(t *testing.T) {
	subject, body := patrol.FormatMail("lab-a", patrol.Changes{})
	if subject != "" || body != "" {
		t.Fatalf("没有变化时主题与正文都必须为空,得到 %q / %q", subject, body)
	}
}

func TestFormatMail_OngoingOnly(t *testing.T) {
	ch := patrol.Changes{Ongoing: []patrol.Finding{{
		Node: "lab-a", ItemID: "old-problem", Status: patrol.StatusProblem,
		Severity: patrol.SeverityWarning, Summary: "还没修好",
	}}}
	subject, body := patrol.FormatMail("lab-a", ch)
	if subject != "" || body != "" {
		t.Fatalf("只有持续项时不该出邮件,得到主题 %q 正文 %q", subject, body)
	}
}

func TestNotify_NoChangesSendsNothing(t *testing.T) {
	sent := 0
	err := patrol.Notify(patrol.Changes{}, func(subject, body string) error {
		sent++
		return nil
	})
	if err != nil {
		t.Fatalf("没有变化时不该报错: %v", err)
	}
	if sent != 0 {
		t.Fatalf("没有变化时发了 %d 封信,期望 0 封", sent)
	}
}

func TestNotify_OngoingOnlySendsNothing(t *testing.T) {
	sent := 0
	err := patrol.Notify(patrol.Changes{Ongoing: []patrol.Finding{{
		Node: "lab-a", ItemID: "old-problem", Status: patrol.StatusProblem,
		Severity: patrol.SeverityWarning, Summary: "还没修好",
	}}}, func(subject, body string) error {
		sent++
		return nil
	})
	if err != nil {
		t.Fatalf("只有持续项时不该报错: %v", err)
	}
	if sent != 0 {
		t.Fatalf("只有持续项时发了 %d 封信,期望 0 封(持续项不打扰)", sent)
	}
}

func TestNotify_ChangesSendsOnce(t *testing.T) {
	var subjects, bodies []string
	err := patrol.Notify(threeGroupChanges(), func(subject, body string) error {
		subjects = append(subjects, subject)
		bodies = append(bodies, body)
		return nil
	})
	if err != nil {
		t.Fatalf("发信失败: %v", err)
	}
	if len(subjects) != 1 || len(bodies) != 1 {
		t.Fatalf("有变化时应恰好发一封,实际 %d 封", len(subjects))
	}
	if !strings.Contains(subjects[0], "lab-a") || !strings.Contains(subjects[0], "新增 1") {
		t.Fatalf("主题没带上节点与变化计数: %q", subjects[0])
	}
	if bodies[0] == "" {
		t.Fatal("正文为空")
	}
}

func TestNotify_SendErrorPropagated(t *testing.T) {
	boom := errors.New("smtp 拒收")
	err := patrol.Notify(threeGroupChanges(), func(subject, body string) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("投递错误必须原样传出去,得到 %v", err)
	}
}

func TestNotify_NilSendWithChanges(t *testing.T) {
	err := patrol.Notify(threeGroupChanges(), nil)
	if err == nil {
		t.Fatal("有变化却没有投递通道时必须报错,不能静默丢信")
	}
}
