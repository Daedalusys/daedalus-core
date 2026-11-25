package mailer_test

// 本文件用一个进程内 SMTP 抓取器钉住投递行为:主题与正文按预期到达、每个收件人都进
// RCPT TO、连不上服务器时报错不吞。抓取器只实现最小会话(EHLO/MAIL/RCPT/DATA/QUIT),
// 不连真实 SMTP、不出本机。

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/mailer"
	"github.com/Daedalusys/daedalus-core/internal/patrol"
)

// mailer.Sender 必须能当 patrol.Sender 用:装配层拿到的投递面就是这个形状。
var _ patrol.Sender = (*mailer.Sender)(nil)

// capturedMail 是抓取器收到的一封信。data 是 DATA 段原文(点转义已还原)。
type capturedMail struct {
	from  string
	rcpts []string
	data  string
}

// header 解析出某个头部字段的原文(未解码)。
func (m capturedMail) header(t *testing.T, key string) string {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(m.data))
	if err != nil {
		t.Fatalf("解析收到的邮件失败: %v\n原文:\n%s", err, m.data)
	}
	return msg.Header.Get(key)
}

// body 返回正文;SMTP 传输用 CRLF,统归一化成 LF 再比较内容。
func (m capturedMail) body(t *testing.T) string {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(m.data))
	if err != nil {
		t.Fatalf("解析收到的邮件失败: %v\n原文:\n%s", err, m.data)
	}
	raw, err := io.ReadAll(msg.Body)
	if err != nil {
		t.Fatalf("读正文失败: %v", err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// smtpCatcher 是进程内的最小 SMTP 服务器。
type smtpCatcher struct {
	ln    net.Listener
	mails chan capturedMail
	mu    sync.Mutex
	seen  []string         // 收到过的命令动词,便于断言「没走到投递」
	cert  *tls.Certificate // 非空时宣告并支持 STARTTLS/AUTH
}

func startSMTPCatcher(t *testing.T) *smtpCatcher {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起 SMTP 抓取器失败: %v", err)
	}
	c := &smtpCatcher{ln: ln, mails: make(chan capturedMail, 4)}
	t.Cleanup(func() { ln.Close() })
	go c.serve()
	return c
}

// startTLSMTPCatcher 起一个宣告 STARTTLS 与 AUTH 的抓取器。自签证书经 SSL_CERT_FILE
// 交给进程默认根池:不改投递实现,也不在生产代码里留测试用的信任开关。
func startTLSMTPCatcher(t *testing.T) *smtpCatcher {
	t.Helper()
	cert, caPath := selfSignedCert(t)
	t.Setenv("SSL_CERT_FILE", caPath)

	c := startSMTPCatcher(t)
	c.cert = &cert
	return c
}

func (c *smtpCatcher) host() string { return "127.0.0.1" }

func (c *smtpCatcher) port() int { return c.ln.Addr().(*net.TCPAddr).Port }

// received 非阻塞取一封已收到的信。
func (c *smtpCatcher) received() (capturedMail, bool) {
	select {
	case m := <-c.mails:
		return m, true
	default:
		return capturedMail{}, false
	}
}

// commands 返回抓取器见过的大写命令动词。
func (c *smtpCatcher) commands() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

func (c *smtpCatcher) serve() {
	for {
		conn, err := c.ln.Accept()
		if err != nil {
			return
		}
		go c.handle(conn)
	}
}

func (c *smtpCatcher) handle(rawConn net.Conn) {
	defer rawConn.Close()
	_ = rawConn.SetDeadline(time.Now().Add(10 * time.Second))

	conn := rawConn
	tp := textproto.NewReader(bufio.NewReader(conn))
	reply := func(s string) { fmt.Fprintf(conn, "%s\r\n", s) }

	reply("220 127.0.0.1 ESMTP 抓取器")
	var cur capturedMail
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(verb)
		c.mu.Lock()
		c.seen = append(c.seen, verb)
		c.mu.Unlock()

		switch verb {
		case "EHLO":
			// 默认不宣告 STARTTLS/AUTH:需要它们的配置必须自己失败,而不是被服务器将就。
			reply("250-127.0.0.1")
			if c.cert != nil {
				reply("250-STARTTLS")
				reply("250-AUTH PLAIN")
			}
			reply("250 8BITMIME")
		case "STARTTLS":
			if c.cert == nil {
				reply("502 5.5.1 不支持 STARTTLS")
				continue
			}
			reply("220 2.0.0 可以开始 TLS")
			tlsConn := tls.Server(conn, &tls.Config{
				Certificates: []tls.Certificate{*c.cert},
				MinVersion:   tls.VersionTLS12,
			})
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			// 升级后的会话重新开始:客户端会再发一次 EHLO。
			conn = tlsConn
			tp = textproto.NewReader(bufio.NewReader(conn))
			cur = capturedMail{}
		case "AUTH":
			reply("235 2.7.0 认证成功")
		case "HELO":
			reply("250 127.0.0.1")
		case "MAIL":
			cur.from = envelopeAddr(arg, "FROM:")
			reply("250 2.1.0 Ok")
		case "RCPT":
			cur.rcpts = append(cur.rcpts, envelopeAddr(arg, "TO:"))
			reply("250 2.1.5 Ok")
		case "DATA":
			reply("354 请开始投递")
			data, err := readData(tp)
			if err != nil {
				return
			}
			cur.data = data
			c.mails <- cur
			cur = capturedMail{}
			reply("250 2.0.0 Ok")
		case "RSET":
			cur = capturedMail{}
			reply("250 2.0.0 Ok")
		case "NOOP":
			reply("250 2.0.0 Ok")
		case "QUIT":
			reply("221 2.0.0 Bye")
			return
		default:
			reply("502 5.5.2 未实现的命令")
		}
	}
}

// readData 读 DATA 段直到单独一行 ".";行首的点转义按 SMTP 规则还原。
func readData(tp *textproto.Reader) (string, error) {
	var b strings.Builder
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return "", err
		}
		if line == "." {
			return b.String(), nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		b.WriteString(line)
		b.WriteString("\r\n")
	}
}

// envelopeAddr 从 "FROM:<a@b> SIZE=1" 这类参数里取出地址。
func envelopeAddr(arg, prefix string) string {
	if len(arg) < len(prefix) || !strings.EqualFold(arg[:len(prefix)], prefix) {
		return ""
	}
	rest := strings.TrimSpace(arg[len(prefix):])
	rest = strings.TrimPrefix(rest, "<")
	if i := strings.Index(rest, ">"); i >= 0 {
		rest = rest[:i]
	}
	return strings.TrimSpace(rest)
}

// decodeHeader 解 RFC 2047 编码的头部值。
func decodeHeader(t *testing.T, raw string) string {
	t.Helper()
	dec := new(mime.WordDecoder)
	out, err := dec.DecodeHeader(raw)
	if err != nil {
		t.Fatalf("解码头部 %q 失败: %v", raw, err)
	}
	return out
}

func testSender(c *smtpCatcher) *mailer.Sender {
	return &mailer.Sender{Config: mailer.Config{
		Host: c.host(),
		Port: c.port(),
		From: "daedalus@example.com",
		To:   []string{"ops@example.com", "oncall@example.com"},
	}}
}

// selfSignedCert 生成 127.0.0.1 的自签证书,并把证书写进临时文件返回给调用方当根池。
func selfSignedCert(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成私钥失败: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("签证书失败: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("序列化私钥失败: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("加载自签证书失败: %v", err)
	}

	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatalf("写临时 CA 失败: %v", err)
	}
	return cert, caPath
}

func TestSender_Send_DeliversSubjectBodyRecipients(t *testing.T) {
	catcher := startSMTPCatcher(t)
	s := testSender(catcher)

	subject := "[Daedalus] lab-a 巡检：新增 1 · 恶化 0 · 恢复 0"
	// 正文里放一行单独的点与一行点开头的内容:投递必须原样到达,不能被当成 DATA 结束符。
	body := "== 新增 (1) ==\n\n* disk-health [warning] /tmp 使用率 92%\n  证据原文:\n" +
		"Filesystem      Size  Used Avail Use% Mounted on\n/dev/vdb1       40G   37G  3.0G  92% /tmp\n.\n..hidden\n"

	if err := s.Send(context.Background(), subject, body); err != nil {
		t.Fatalf("投递失败: %v", err)
	}

	got, ok := catcher.received()
	if !ok {
		t.Fatalf("SMTP 抓取器没收到邮件,见过的命令: %v", catcher.commands())
	}

	if decoded := decodeHeader(t, got.header(t, "Subject")); decoded != subject {
		t.Errorf("主题 = %q,期望 %q", decoded, subject)
	}
	if gotBody := got.body(t); !strings.Contains(gotBody, body) {
		t.Errorf("正文没有原样到达\n期望包含:\n%s\n实际正文:\n%s", body, gotBody)
	}
	for _, rcpt := range s.Config.To {
		if !slices.Contains(got.rcpts, rcpt) {
			t.Errorf("RCPT TO 缺 %q,实际 %v", rcpt, got.rcpts)
		}
	}
	if !strings.Contains(got.from, s.Config.From) {
		t.Errorf("MAIL FROM = %q,期望含 %q", got.from, s.Config.From)
	}
	if toHeader := got.header(t, "To"); !strings.Contains(toHeader, "ops@example.com") {
		t.Errorf("To 头部 = %q,期望列出收件人", toHeader)
	}
}

func TestSender_Send_DialErrorIsNotSwallowed(t *testing.T) {
	// 先占一个端口再放掉:这个端口上不会有 SMTP 服务器。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	s := &mailer.Sender{Config: mailer.Config{
		Host: "127.0.0.1",
		Port: port,
		From: "daedalus@example.com",
		To:   []string{"ops@example.com"},
	}}
	if err := s.Send(context.Background(), "主题", "正文"); err == nil {
		t.Fatal("连不上 SMTP 时应返回错误,实际返回 nil")
	}
}

func TestSender_Send_RefusesStartTLSWhenServerCannot(t *testing.T) {
	catcher := startSMTPCatcher(t)
	s := testSender(catcher)
	s.Config.StartTLS = true // 抓取器不宣告 STARTTLS:必须拒绝明文投递,不能将就。

	err := s.Send(context.Background(), "主题", "正文")
	if err == nil {
		t.Fatal("服务器不支持 STARTTLS 时应返回错误,实际返回 nil")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("错误应点明 STARTTLS,实际: %v", err)
	}
	if got, ok := catcher.received(); ok {
		t.Errorf("被拒的投递不该送达,实际收到 %+v", got)
	}
}

func TestSender_Send_RejectsIncompleteConfig(t *testing.T) {
	cases := map[string]mailer.Config{
		"缺 host":  {Port: 25, From: "a@example.com", To: []string{"b@example.com"}},
		"缺端口":     {Host: "127.0.0.1", From: "a@example.com", To: []string{"b@example.com"}},
		"缺发件人":    {Host: "127.0.0.1", Port: 25, To: []string{"b@example.com"}},
		"缺收件人":    {Host: "127.0.0.1", Port: 25, From: "a@example.com"},
		"发件人不是邮箱": {Host: "127.0.0.1", Port: 25, From: "not-an-address", To: []string{"b@example.com"}},
		"收件人不是邮箱": {Host: "127.0.0.1", Port: 25, From: "a@example.com", To: []string{"not-an-address"}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			c := cfg
			s := &mailer.Sender{Config: c}
			if err := s.Send(context.Background(), "主题", "正文"); err == nil {
				t.Fatalf("配置 %+v 应被拒,实际返回 nil", c)
			}
		})
	}
}

func TestSender_Send_RejectsHeaderInjection(t *testing.T) {
	catcher := startSMTPCatcher(t)
	s := testSender(catcher)
	if err := s.Send(context.Background(), "主题\r\nBcc: evil@example.com", "正文"); err == nil {
		t.Fatal("主题里的换行应被拒,实际返回 nil")
	}
}

// STARTTLS + 认证是生产链路(587 端口)的真实形态:升级成功后凭据才发出,
// 邮件经 TLS 到达。
func TestSender_Send_StartTLSAndAuth(t *testing.T) {
	catcher := startTLSMTPCatcher(t)
	s := testSender(catcher)
	s.Config.StartTLS = true
	s.Config.Username = "daedalus"
	s.Config.Password = "secret"

	subject := "[Daedalus] lab-a 巡检：新增 1 · 恶化 0 · 恢复 0"
	if err := s.Send(context.Background(), subject, "正文"); err != nil {
		t.Fatalf("STARTTLS 投递失败: %v", err)
	}

	got, ok := catcher.received()
	if !ok {
		t.Fatalf("SMTP 抓取器没收到邮件,见过的命令: %v", catcher.commands())
	}
	if decoded := decodeHeader(t, got.header(t, "Subject")); decoded != subject {
		t.Errorf("主题 = %q,期望 %q", decoded, subject)
	}
	if !slices.Contains(catcher.commands(), "STARTTLS") {
		t.Errorf("投递应走过 STARTTLS,实际命令: %v", catcher.commands())
	}
	if !slices.Contains(catcher.commands(), "AUTH") {
		t.Errorf("配了凭据就应认证,实际命令: %v", catcher.commands())
	}
}

// 非本机回环 + 明文 + 凭据 = 拒绝:凭据不能裸奔。
func TestSender_Send_RefusesPlaintextCredentials(t *testing.T) {
	s := &mailer.Sender{Config: mailer.Config{
		Host:     "smtp.example.com",
		Port:     25,
		Username: "daedalus",
		Password: "secret",
		From:     "daedalus@example.com",
		To:       []string{"ops@example.com"},
	}}
	err := s.Send(context.Background(), "主题", "正文")
	if err == nil {
		t.Fatal("明文链路上的凭据应被拒,实际返回 nil")
	}
	if !strings.Contains(err.Error(), "凭据") {
		t.Errorf("错误应点明拒绝投递凭据,实际: %v", err)
	}
}
