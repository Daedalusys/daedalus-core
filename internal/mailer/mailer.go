// mailer.go 用标准库把一封邮件投到 SMTP 服务器。
//
// 只做投递:不重试、不排队、不落盘 —— 失败原样返回,由调用方决定记审计还是下一轮再发。
// 凭据不进配置文件,装配层从 systemd LoadCredential 读出后填进 Config。
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
	"unicode/utf8"
)

// dialTimeout 是连接与握手阶段的兜底时限。SMTP 会话没有 ctx 支持,
// 不设时限就会在对端不说话时一直挂着。
const dialTimeout = 30 * time.Second

// Config 是一次投递需要的全部参数。
//
// StartTLS 为真表示「必须升级到 TLS」:服务器不支持就失败,不降级明文投递。
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       []string
	StartTLS bool
}

// Sender 按配置投递邮件。零值不可用:至少要填 Host / Port / From / To。
type Sender struct {
	Config Config
}

// Send 把一封邮件投出去。投递失败一律返回错误,调用方不得把失败当成功吞掉。
//
// ctx 决定连接阶段的取消与截止时间;SMTP 会话本身在连接建立后不再看 ctx,
// 靠连接截止时间兜底。
func (s *Sender) Send(ctx context.Context, subject, body string) error {
	from, to, err := s.Config.envelope()
	if err != nil {
		return err
	}
	if err := s.Config.checkCredentials(); err != nil {
		return err
	}
	if strings.ContainsAny(subject, "\r\n") {
		return errors.New("邮件主题不能含换行(会被当成头注入)")
	}

	addr := net.JoinHostPort(s.Config.Host, strconv.Itoa(s.Config.Port))
	conn, err := dial(ctx, addr)
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器 %s 失败: %w", addr, err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, s.Config.Host)
	if err != nil {
		return fmt.Errorf("SMTP 握手失败: %w", err)
	}
	if err := startTLS(client, s.Config); err != nil {
		return err
	}
	if err := authenticate(client, s.Config); err != nil {
		return err
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("SMTP MAIL FROM 失败: %w", err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("SMTP RCPT TO %s 失败: %w", rcpt, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA 失败: %w", err)
	}
	if _, err := w.Write(message(s.Config, from, to, subject, body)); err != nil {
		return fmt.Errorf("SMTP 写入正文失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP 结束投递失败: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("SMTP 退出会话失败: %w", err)
	}
	return nil
}

// envelope 校验并规范化发件人与收件人地址。
//
// 地址只取 mail.Address.Address:带显示名的写法原样塞进 MAIL FROM 会让
// 反垃圾网关拒收,显示名也不该由投递面替调用方决定。
func (c Config) envelope() (string, []string, error) {
	if c.Host == "" {
		return "", nil, errors.New("SMTP 配置缺少 host")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return "", nil, fmt.Errorf("SMTP 配置的端口 %d 非法", c.Port)
	}
	from, err := parseAddress(c.From)
	if err != nil {
		return "", nil, fmt.Errorf("SMTP 发件人地址非法: %w", err)
	}
	if len(c.To) == 0 {
		return "", nil, errors.New("SMTP 配置缺少收件人")
	}
	to := make([]string, 0, len(c.To))
	for _, rcpt := range c.To {
		addr, err := parseAddress(rcpt)
		if err != nil {
			return "", nil, fmt.Errorf("SMTP 收件人地址非法: %w", err)
		}
		to = append(to, addr)
	}
	return from, to, nil
}

// parseAddress 校验单个地址,空地址与带换行的地址一律拒。
func parseAddress(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("地址为空")
	}
	if strings.ContainsAny(raw, "\r\n") {
		return "", errors.New("地址含换行")
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return "", err
	}
	return addr.Address, nil
}

// dial 建立到 SMTP 服务器的连接,并把 ctx 的截止时间落到连接上。
func dial(ctx context.Context, addr string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(dialTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// startTLS 按配置决定是否升级到 TLS。配置要求 TLS 而服务器不支持时报错 ——
// 明文投递凭据与明文投递正文都不是「降级」能接受的事。
func startTLS(client *smtp.Client, cfg Config) error {
	supported, _ := client.Extension("STARTTLS")
	if !cfg.StartTLS {
		return nil
	}
	if !supported {
		return fmt.Errorf("SMTP 服务器 %s 不支持 STARTTLS,拒绝明文投递", cfg.Host)
	}
	if err := client.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("SMTP STARTTLS 握手失败: %w", err)
	}
	return nil
}

// checkCredentials 在连接前拒绝「明文链路 + 凭据」的组合:凭据不能裸奔。
// 本机回环放行,与 net/smtp 的 PlainAuth 同一条底线(本地靶场是常见测试形态)。
func (c Config) checkCredentials() error {
	if c.Username == "" || c.StartTLS || isLoopback(c.Host) {
		return nil
	}
	return fmt.Errorf("SMTP 主机 %s 不是本机回环且未启用 starttls,拒绝明文投递凭据", c.Host)
}

// authenticate 用配置的凭据登录。没有用户名就不认证(免密投递是常见形态)。
func authenticate(client *smtp.Client, cfg Config) error {
	if cfg.Username == "" {
		return nil
	}
	supported, _ := client.Extension("AUTH")
	if !supported {
		return fmt.Errorf("SMTP 服务器 %s 不支持 AUTH,无法用配置的凭据登录", cfg.Host)
	}
	if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
		return fmt.Errorf("SMTP 认证失败: %w", err)
	}
	return nil
}

// isLoopback 判断主机是否本机回环。本机回环的明文投递是常见测试形态。
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// message 拼出 RFC 5322 邮件:头部 + 空行 + 正文。
//
// 正文按字节原样带上:SMTP 传输层负责行尾与点转义,这里不做任何改写。
func message(cfg Config, from string, to []string, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", encodeHeader(subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

// encodeHeader 对非 ASCII 头部值做 RFC 2047 编码,纯 ASCII 保持原样便于人读。
func encodeHeader(v string) string {
	if isASCII(v) {
		return v
	}
	return mime.QEncoding.Encode("UTF-8", v)
}

// isASCII 判断字符串是否全是非 ASCII 字节。
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
