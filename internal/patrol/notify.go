// notify.go 把一轮变化渲染成邮件并交给投递口。
//
// 硬约束:只有「新增 / 恶化 / 恢复」才发信,持续项与空变化一律不发 ——
// 每轮重复同样的告警会让人学会忽略邮件。证据在这封信里只做搬运:
// 工具返回原文逐字贴出,不摘要、不改写、不转义成 HTML,否则「证据」就成了转述。
package patrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// 邮件文本的固定词。三组标题同时是主题里的计数标签。
const (
	mailPrefix = "[Daedalus]"
	mailTitle  = "巡检"
	groupAdded = "新增"
	groupWorse = "恶化"
	groupBack  = "恢复"
)

// Sender 是投递口的形状;mailer.Sender 的方法满足它。
//
// 本包只依赖这个形状,不认识 SMTP 参数 —— 地址、凭据、收件人由装配方给。
type Sender interface {
	Send(ctx context.Context, subject, body string) error
}

// Notify 在存在变化时发一封邮件,没有变化时什么都不做。
//
// send 由调用方提供(通常是绑好 ctx 的 mailer.Sender.Send 适配)。
// 投递失败原样上抛:吞掉它就等于「巡检正常」的假象。
func Notify(changes Changes, send func(subject, body string) error) error {
	subject, body := FormatMail(changesNode(changes), changes)
	if subject == "" {
		return nil
	}
	if send == nil {
		return errors.New("patrol: 本轮有变化,但没有配置邮件投递口")
	}
	return send(subject, body)
}

// FormatMail 把变化渲染成主题与正文;没有任何变化时返回两个空串。
//
// node 为空时不硬塞占位词:多节点汇总或调用方未给节点名时,
// 主题保留「巡检」而不出现一段空白。
func FormatMail(node string, changes Changes) (subject, body string) {
	if !changes.hasChanges() {
		return "", ""
	}

	head := mailPrefix + " " + mailTitle
	if node != "" {
		head = mailPrefix + " " + node + " " + mailTitle
	}
	subject = fmt.Sprintf("%s：%s %d · %s %d · %s %d",
		head, groupAdded, len(changes.Added), groupWorse, len(changes.Worsened), groupBack, len(changes.Recovered))

	var b strings.Builder
	if node != "" {
		fmt.Fprintf(&b, "节点 %s 巡检结果\n", node)
	} else {
		b.WriteString("巡检结果\n")
	}
	writeGroup(&b, groupAdded, changes.Added)
	writeGroup(&b, groupWorse, changes.Worsened)
	writeGroup(&b, groupBack, changes.Recovered)
	return subject, b.String()
}

// writeGroup 渲染一组发现。组标题始终打印(空组也打印计数),
// 让读者一眼看出「这轮确实没有恢复项」而不是「恢复一栏被漏了」。
func writeGroup(b *strings.Builder, title string, items []Finding) {
	fmt.Fprintf(b, "\n%s（%d 项）\n", title, len(items))
	for _, f := range items {
		writeItem(b, f)
	}
}

func writeItem(b *strings.Builder, f Finding) {
	if f.Severity == "" {
		fmt.Fprintf(b, "- %s %s\n", f.ItemID, f.Summary)
	} else {
		fmt.Fprintf(b, "- %s [%s] %s\n", f.ItemID, f.Severity, f.Summary)
	}
	for i, ev := range f.Evidence {
		writeEvidence(b, i+1, ev)
	}
}

// writeEvidence 贴一次工具调用的原文。
//
// Output 前面只加标记、不缩进:缩进会改变每一行的字节,原文就不再是原文。
func writeEvidence(b *strings.Builder, n int, ev Evidence) {
	fmt.Fprintf(b, "  证据 %d：%s（返回码 %d）\n", n, ev.Tool, ev.ReturnCode)
	if ev.Input != "" {
		fmt.Fprintf(b, "  参数：%s\n", ev.Input)
	}
	b.WriteString(ev.Output)
	if ev.Output != "" && !strings.HasSuffix(ev.Output, "\n") {
		b.WriteString("\n")
	}
}

// hasChanges 判断有没有需要打扰人的变化。Ongoing 不算:它上一轮已经报过了。
func (c Changes) hasChanges() bool {
	return len(c.Added) > 0 || len(c.Worsened) > 0 || len(c.Recovered) > 0
}

// changesNode 从发现里取节点名 —— Finding 自带 Node,调用方不必另传。
// 按「变化组优先」扫描:持续项不进邮件,只在前三组都给不出节点时才用它兜底。
func changesNode(c Changes) string {
	for _, group := range [][]Finding{c.Added, c.Worsened, c.Recovered, c.Ongoing} {
		for _, f := range group {
			if f.Node != "" {
				return f.Node
			}
		}
	}
	return ""
}
