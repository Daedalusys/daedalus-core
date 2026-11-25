package patrol_test

// 本文件钉住「只有变化才发信」与邮件文本的信任基础:
// 三组标题齐全、计数进主题、证据原文逐字出现在正文里(不摘要、不改写、不转义),
// 持续项不进任何一组。

import (
	"errors"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/patrol"
)

// 刻意带前导空格、制表符与换行:任何「美化」都会让这段原文不再是原文。
const (
	addedOutput     = "  Filesystem      Size  Used Avail Use%\n/dev/vdb1       100G  100G     0 100% /tmp"
	worsenedOutput  = "Unit etcd.service entered failed state.\nfailed"
	recoveredOutput = "active"
)

func threeGroupChanges() patrol.Changes {
	return patrol.Changes{
		Added: []patrol.Finding{{
			Node: "lab-a", ItemID: "disk-health", Status: patrol.StatusProblem,
			Severity: patrol.SeverityCritical, Summary: "/tmp 已写满",
			Evidence: []patrol.Evidence{{
				Tool: "shell_exec", Input: `{"command":["df","-h","/tmp"]}`,
				Output: addedOutput, ReturnCode: 1,
			}},
		}},
		Worsened: []patrol.Finding{{
			Node: "lab-a", ItemID: "service-health", Status: patrol.StatusProblem,
			Severity: patrol.SeverityCritical, Summary: "etcd 从 warning 变 critical",
			Evidence: []patrol.Evidence{{
				Tool: "service_query", Input: `{"name":"etcd"}`,
				Output: worsenedOutput, ReturnCode: 0,
			}},
		}},
		Recovered: []patrol.Finding{{
			Node: "lab-a", ItemID: "proc-health", Status: patrol.StatusProblem,
			Severity: patrol.SeverityWarning, Summary: "多余监听已消失",
			Evidence: []patrol.Evidence{{
				Tool: "shell_exec", Input: `{"command":["ss","-ltnp"]}`,
				Output: recoveredOutput, ReturnCode: 0,
			}},
		}},
	}
}

func TestFormatMail_ThreeGroups(t *testing.T) {
	subject, body := patrol.FormatMail("lab-a", threeGroupChanges())

	if !strings.Contains(subject, "lab-a") {
		t.Errorf("主题应含节点名,实际 %q", subject)
	}
	for _, want := range []string{"新增 1", "恶化 1", "恢复 1"} {
		if !strings.Contains(subject, want) {
			t.Errorf("主题应含 %q,实际 %q", want, subject)
		}
	}

	for _, want := range []string{"新增", "恶化", "恢复"} {
		if !strings.Contains(body, want) {
			t.Errorf("正文应含分组标题 %q,实际:\n%s", want, body)
		}
	}
	for _, want := range []string{"disk-health", "service-health", "proc-health", "/tmp 已写满"} {
		if !strings.Contains(body, want) {
			t.Errorf("正文应含 %q,实际:\n%s", want, body)
		}
	}
	// 证据原文逐字出现:整段(含前导空格与内部换行)必须是正文的子串。
	for _, want := range []string{addedOutput, worsenedOutput, recoveredOutput} {
		if !strings.Contains(body, want) {
			t.Errorf("证据原文未逐字出现:%q\n正文:\n%s", want, body)
		}
	}
}

func TestFormatMail_NoChangesReturnsEmpty(t *testing.T) {
	subject, body := patrol.FormatMail("lab-a", patrol.Changes{})
	if subject != "" || body != "" {
		t.Errorf("没有变化应返回两个空串,实际 subject=%q body=%q", subject, body)
	}
}

func TestFormatMail_OngoingOnlyReturnsEmpty(t *testing.T) {
	ch := patrol.Changes{Ongoing: []patrol.Finding{{
		Node: "lab-a", ItemID: "disk-health", Status: patrol.StatusProblem,
		Severity: patrol.SeverityWarning, Summary: "还是满的",
	}}}
	subject, body := patrol.FormatMail("lab-a", ch)
	if subject != "" || body != "" {
		t.Errorf("只有持续项时不该有邮件,实际 subject=%q body=%q", subject, body)
	}
	if strings.Contains(body, "还是满的") {
		t.Error("持续项的 summary 不应进邮件")
	}
}

func TestNotify_NoChangesSendsNothing(t *testing.T) {
	sent := 0
	err := patrol.Notify(patrol.Changes{}, func(subject, body string) error {
		sent++
		return nil
	})
	if err != nil {
		t.Fatalf("没有变化时 Notify 应返回 nil,实际 %v", err)
	}
	if sent != 0 {
		t.Errorf("没有变化不该发信,实际发了 %d 封", sent)
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
		t.Fatalf("Notify 返回错误: %v", err)
	}
	if len(subjects) != 1 {
		t.Fatalf("有变化应恰好发一封,实际 %d 封", len(subjects))
	}
	// 节点名由发现自带的 Node 推导,调用方不必另传。
	if !strings.Contains(subjects[0], "lab-a") {
		t.Errorf("主题应含从发现推导出的节点名,实际 %q", subjects[0])
	}
	if !strings.Contains(bodies[0], addedOutput) {
		t.Error("正文应带证据原文")
	}
}

func TestNotify_PropagatesSendError(t *testing.T) {
	wantErr := errors.New("smtp 挂了")
	err := patrol.Notify(threeGroupChanges(), func(subject, body string) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("投递错误必须原样上抛,实际 %v", err)
	}
}
