// mailer.go 是巡检通知的 SMTP 投递口。
//
// 只用标准库:一次投递是一条独立连接,失败即返回错误,没有重试与队列 ——
// 「投递失败」必须让调用方看见(巡检链路会把它记成发现),而不是在后台悄悄烂掉。
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// defaultTimeout 是没有 ctx 截止时间时的单次投递上限:
// 投递是巡检链路的一环,不能因为对端不响应把整轮巡检挂住。
const defaultTimeout = 30 * time.Second

// Config 是一次投递所需的全部参数。凭据由装配方从 LoadCredential 读入,
// 不落配置文件;这里只接收值。
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       []string
	StartTLS bool
}

// Sender 是配置好的投递口;零值以外的字段都由 Config 提供。
type Sender struct {
	Config Config
}

// Send 投递一封纯文本邮件。任何一步失败都返回错误,不吞。
func (s *Sender) Send(ctx context.Context, subject, body string) error {
	cfg := s.Config
	if err := cfg.validate(); err != nil {
		return err
	}
	// 先判取消:拨号前 ctx 已失效时直接给出可判定的 context 错误。
	if err := ctx.Err(); err != nil {
		return err
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultTimeout)
	}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mailer: 连接 SMTP %s 失败: %w", addr, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("mailer: 设置连接期限失败: %w", err)
	}
	// 拨号后 ctx 才失效时也立刻断开,不用等到默认期限。
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stopCancel()

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("mailer: SMTP 握手失败: %w", err)
	}
	defer client.Close()

	if cfg.StartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("mailer: SMTP 服务器 %s 未提供 STARTTLS,拒绝明文投递", addr)
		}
		tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("mailer: STARTTLS 失败: %w", err)
		}
	}
	if cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("mailer: SMTP 认证失败: %w", err)
		}
	}

	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("mailer: MAIL FROM 被拒: %w", err)
	}
	for _, to := range cfg.To {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("mailer: RCPT TO %s 被拒: %w", to, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("mailer: DATA 被拒: %w", err)
	}
	if _, err := w.Write(buildMessage(cfg, subject, body)); err != nil {
		_ = w.Close()
		return fmt.Errorf("mailer: 写正文失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: 提交正文失败: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("mailer: QUIT 失败: %w", err)
	}
	return nil
}

// validate 在拨号前拒掉残缺配置:少一个收件人也要报错,不能投一半。
func (c Config) validate() error {
	switch {
	case strings.TrimSpace(c.Host) == "":
		return errors.New("mailer: 未配置 smtp host")
	case c.Port <= 0 || c.Port > 65535:
		return fmt.Errorf("mailer: smtp 端口 %d 非法", c.Port)
	case strings.TrimSpace(c.From) == "":
		return errors.New("mailer: 未配置发件人 from")
	case len(c.To) == 0:
		return errors.New("mailer: 未配置收件人 to")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return fmt.Errorf("mailer: 发件人 %q 不是合法地址: %w", c.From, err)
	}
	for _, to := range c.To {
		if _, err := mail.ParseAddress(to); err != nil {
			return fmt.Errorf("mailer: 收件人 %q 不是合法地址: %w", to, err)
		}
	}
	// 凭据只能走加密连接:明文 AUTH 等于把密码交给链路上的任何一跳。
	if c.Username != "" && !c.StartTLS {
		return errors.New("mailer: 配置了 smtp 用户名却未开启 starttls")
	}
	return nil
}

// buildMessage 拼一封纯文本邮件。主题含中文,必须按 RFC 2047 编码 ——
// 否则对端会按 latin-1 解出乱码。正文不加 Content-Transfer-Encoding 之外的加工:
// 证据原文要原样到达收件人。
func buildMessage(cfg Config, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", cfg.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(cfg.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}
