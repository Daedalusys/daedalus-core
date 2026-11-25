// mailer.go:巡检邮件的 SMTP 投递通道。
//
// 只用标准库 net/smtp + crypto/tls:投递面只需要 EHLO/AUTH/MAIL/RCPT/DATA 这几个动词,
// 为它引一个第三方库不划算。配置(含凭据)由调用方注入,本包不读凭据、不落盘、不重试 ——
// 重试策略归调度方,这里失败就把错误交回去。
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultPort 是配置省略 port 时的取值:SMTP 的标准端口。
	defaultPort = 25
	// dialTimeout 防的是「连不上也不返回」:监控通道的投递不能把一轮巡检钉死在这里。
	dialTimeout = 30 * time.Second
	// sendTimeout 是调用方 ctx 没有期限时的整体兜底。会话中途卡住(对端不回 250)
	// 没有别的出口,只能靠自己设的 deadline 断开。
	sendTimeout = 60 * time.Second
)

// Config 是一次投递需要的全部参数。
//
// Username 为空即不做认证;凭据由调用方从 systemd LoadCredential 读出后填进来。
// StartTLS 为真时服务器必须提供 STARTTLS,否则拒发 —— 宁可报错也不把正文明文发出去。
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       []string
	StartTLS bool
}

// Sender 按 Config 投递一封纯文本邮件。零值不可用,至少要有 Host/From/To。
type Sender struct {
	Config Config
}

// Send 把 subject 与 body 投给全部收件人,任何一步失败都原样返回错误。
func (s *Sender) Send(ctx context.Context, subject, body string) error {
	cfg := s.Config
	if err := cfg.validate(); err != nil {
		return err
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.port()))
	conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器 %s 失败: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	// 读写都要有期限:截止时间取自 ctx,ctx 没给就兜一个上限。
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(sendTimeout)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("设置 SMTP 会话期限失败: %w", err)
	}
	// ctx 取消时只能关连接来打断阻塞中的读写(标准库的 SMTP 客户端不认 ctx)。
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP 会话建立失败: %w", err)
	}
	defer func() { _ = c.Close() }()

	if cfg.StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("SMTP 服务器没有提供 STARTTLS,拒绝以明文投递")
		}
		if err := c.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("STARTTLS 升级失败: %w", err)
		}
	}
	if cfg.Username != "" {
		// PlainAuth 在非 TLS 且非本机连接上会自己拒绝发送凭据,这里不重复判。
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}

	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("MAIL FROM %s 被拒: %w", cfg.From, err)
	}
	for _, rcpt := range cfg.To {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("RCPT TO %s 被拒: %w", rcpt, err)
		}
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA 被拒: %w", err)
	}
	if _, err := w.Write([]byte(buildMessage(cfg, subject, body))); err != nil {
		// 写了一半就断:此时不能关 Data 的写入端,否则对端会把半截信当成完整邮件收下。
		_ = c.Close()
		return fmt.Errorf("写入邮件内容失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("提交邮件内容失败: %w", err)
	}
	// 邮件已被服务器收下,QUIT 失败不该把投递结果改判成失败。
	_ = c.Quit()
	return nil
}

// buildMessage 拼一封纯文本邮件。正文里的中文与证据原文按 UTF-8 原样写:
// 任何转义、折行或缩进都会让「证据是工具返回原文的子串」这条回查线索断掉;
// 行首点号与换行由 SMTP 的 dot-stuffing 层处理,本函数不碰。
func buildMessage(cfg Config, subject, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", cfg.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(cfg.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return b.String()
}

// validate 在连接前把坏配置挡掉:少一个收件人就不该去敲服务器,
// 更不该把「其实没人收到」当成投递成功。
func (c Config) validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("SMTP 配置缺少 host")
	}
	if strings.TrimSpace(c.From) == "" {
		return errors.New("SMTP 配置缺少 from")
	}
	if len(c.To) == 0 {
		return errors.New("SMTP 配置没有收件人")
	}
	for i, rcpt := range c.To {
		if strings.TrimSpace(rcpt) == "" {
			return fmt.Errorf("SMTP 配置的第 %d 个收件人为空", i+1)
		}
	}
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("SMTP 端口 %d 超出 0..65535", c.Port)
	}
	return nil
}

func (c Config) port() int {
	if c.Port == 0 {
		return defaultPort
	}
	return c.Port
}
