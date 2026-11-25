// notify.go 把一轮变化渲染成邮件并决定发不发。
//
// 只有「新增 / 恶化 / 恢复」值得打扰人:持续项不进邮件,没有这三类时连一封都不发。
// 正文里的证据一律照抄工具返回原文 —— 邮件是给人核对事实的,不是给模型复述的。
package patrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Sender 是邮件投递面,mailer.Sender 满足它。声明成接口是为了让本包不依赖具体
// 传输实现(SMTP 参数由装配层持有),测试里可以用一个函数替代。
type Sender interface {
	Send(ctx context.Context, subject, body string) error
}

// Notify 在本轮有变化时发一封信,返回投递错误(不吞)。
//
// 节点从发现里取:一轮变化都属于同一台机器,调用方不必另传一个可能与发现不符的名字。
func Notify(changes Changes, send func(subject, body string) error) error {
	subject, body := FormatMail(nodeOf(changes), changes)
	if subject == "" {
		return nil
	}
	if send == nil {
		// 装配漏了投递面时,最坏的结果不是漏发而是 panic 让整轮状态回滚。
		return errors.New("巡检有变化,但没有装配邮件投递函数")
	}
	return send(subject, body)
}

// FormatMail 渲染主题与正文;没有三类变化时返回两个空串,这一个信号就代表「不发信」。
func FormatMail(node string, changes Changes) (subject, body string) {
	added, worsened, recovered := len(changes.Added), len(changes.Worsened), len(changes.Recovered)
	if added+worsened+recovered == 0 {
		return "", ""
	}

	prefix := ""
	if node != "" {
		prefix = node + " "
	}
	subject = fmt.Sprintf("[Daedalus] %s巡检：新增 %d · 恶化 %d · 恢复 %d",
		prefix, added, worsened, recovered)

	var b strings.Builder
	writeGroup(&b, "新增", changes.Added)
	writeGroup(&b, "恶化", changes.Worsened)
	writeGroup(&b, "恢复", changes.Recovered)
	return subject, b.String()
}

// writeGroup 写一组发现。空组整段省略:标题只在有内容时出现,免得正文全是空小节。
func writeGroup(b *strings.Builder, title string, findings []Finding) {
	if len(findings) == 0 {
		return
	}
	fmt.Fprintf(b, "== %s (%d) ==\n\n", title, len(findings))
	for _, f := range findings {
		fmt.Fprintf(b, "* %s [%s] %s\n", f.ItemID, f.Severity, f.Summary)
		for _, ev := range f.Evidence {
			fmt.Fprintf(b, "  工具: %s\n", ev.Tool)
			if ev.Input != "" {
				fmt.Fprintf(b, "  参数: %s\n", ev.Input)
			}
			fmt.Fprintf(b, "  返回码: %d\n", ev.ReturnCode)
			b.WriteString("  证据原文:\n")
			b.WriteString(ev.Output)
			if !strings.HasSuffix(ev.Output, "\n") {
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}
}

// nodeOf 按组顺序取第一条发现所属节点。三类都为空时返回空串 —— 调用方拿到空串
// 也只会得到空邮件,不构成误发。
func nodeOf(changes Changes) string {
	for _, group := range [][]Finding{changes.Added, changes.Worsened, changes.Recovered} {
		for _, f := range group {
			if f.Node != "" {
				return f.Node
			}
		}
	}
	return ""
}
