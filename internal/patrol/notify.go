// notify.go:把一轮巡检的「变化」渲染成邮件并投递。
//
// 只有新增 / 恶化 / 恢复三类算变化;持续项不进邮件 —— 同一件未修好的事每轮重复
// 打扰,值班人很快就会把整条通道静音。正文里的证据输出按 byte 原样贴出(不缩进、
// 不折行),因为「证据必须是工具返回原文的子串」是这条链路唯一可回查的东西。
package patrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Sender 是邮件投递面。patrol 不关心 SMTP 参数怎么来 —— 调用方用它自己的通道
// (mailer.Sender 的 Send)满足本接口,配置、会话与超时都在那边。
//
// Notify 收的是不带 ctx 的闭包:一轮巡检的超时归调度方,投递只是它的一段;
// 需要 ctx 的实现在装配处绑好再交进来。
type Sender interface {
	Send(ctx context.Context, subject, body string) error
}

// Notify 在有变化时发一封邮件,没有变化时什么都不做。
//
// 返回 send 的错误原样:投递失败不能被当成「没有变化」静默吞掉,否则这一轮的
// 发现就只活在日志里。send 为 nil 且确实有变化时直接报错,不发也不假装发过。
func Notify(changes Changes, send func(subject, body string) error) error {
	subject, body := FormatMail(changesNode(changes), changes)
	if subject == "" && body == "" {
		return nil
	}
	if send == nil {
		return errors.New("本轮有变化但没有配置投递通道")
	}
	return send(subject, body)
}

// FormatMail 渲染主题与正文;没有任何变化时返回两个空串(调用方据此决定不发信)。
//
// subject 只需节点与三个计数,收信人扫一眼就知道该不该细看。正文分三组,组内每条
// 一行「item_id + 严重级 + summary」,紧跟其证据原文。
func FormatMail(node string, changes Changes) (subject, body string) {
	total := len(changes.Added) + len(changes.Worsened) + len(changes.Recovered)
	if total == 0 {
		return "", ""
	}

	subject = fmt.Sprintf("[Daedalus] %s 巡检：新增 %d · 恶化 %d · 恢复 %d",
		node, len(changes.Added), len(changes.Worsened), len(changes.Recovered))

	var b strings.Builder
	fmt.Fprintf(&b, "节点: %s\n", node)
	for _, g := range []struct {
		title    string
		findings []Finding
	}{
		{"新增", changes.Added},
		{"恶化", changes.Worsened},
		{"恢复", changes.Recovered},
	} {
		// 空组也出标题:收信人一眼能确认「这一轮没有恶化」,而不是猜正文为什么只有一组。
		fmt.Fprintf(&b, "\n== %s (%d) ==\n", g.title, len(g.findings))
		for _, f := range g.findings {
			writeFinding(&b, f)
		}
	}
	return subject, b.String()
}

// writeFinding 渲染一条发现。证据输出前留一个说明行,输出本身从行首开始 ——
// 加任何缩进都会让原文不再是收信人复制回查时的那一段。
func writeFinding(b *strings.Builder, f Finding) {
	fmt.Fprintf(b, "- %s [%s] %s\n", f.ItemID, f.Severity, f.Summary)
	for _, ev := range f.Evidence {
		fmt.Fprintf(b, "  证据 tool=%s returncode=%d", ev.Tool, ev.ReturnCode)
		if ev.Input != "" {
			fmt.Fprintf(b, " input=%s", ev.Input)
		}
		b.WriteString("\n")
		if strings.TrimSpace(ev.Output) == "" {
			// 空输出不该占一段空白:收信人会以为信被截断或证据被吞了。
			b.WriteString("  (证据输出为空)\n")
			continue
		}
		b.WriteString(ev.Output)
		if !strings.HasSuffix(ev.Output, "\n") {
			b.WriteString("\n")
		}
	}
}

// changesNode 取这一轮的节点名。一轮巡检的发现共享同一节点(状态文件按节点分文件),
// 先出现的非空节点名即代表这一轮;变化条目可能只有恢复项,所以四组都要看。
func changesNode(changes Changes) string {
	for _, group := range [][]Finding{changes.Added, changes.Worsened, changes.Recovered, changes.Ongoing} {
		for _, f := range group {
			if f.Node != "" {
				return f.Node
			}
		}
	}
	return "未知节点"
}
