package mailer_test

// 本文件用进程内最小 SMTP 抓取器钉住投递事实:主题与正文原样到达、
// To 里每个收件人都进了 RCPT TO、连不上服务器时错误不被吞掉。
// 不连真实 SMTP、不出网 —— 抓取器只监听 127.0.0.1 的临时端口。

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/mailer"
)

// smtpCapture 是一个只够让 net/smtp 走完一轮投递的假服务器。
type smtpCapture struct {
	ln net.Listener

	mu       sync.Mutex
	mailFrom string
	rcpts    []string
	data     string
}

// startSMTP 起假服务器并返回抓取器;测试结束自动关监听。
func startSMTP(t *testing.T) *smtpCapture {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听 127.0.0.1 失败: %v", err)
	}
	c := &smtpCapture{ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go c.serve(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return c
}

func (c *smtpCapture) addr() (string, int) {
	host, portStr, _ := net.SplitHostPort(c.ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func (c *smtpCapture) config() mailer.Config {
	host, port := c.addr()
	return mailer.Config{
		Host: host, Port: port,
		From: "patrol@lab-a.example",
		To:   []string{"oncall@example.com", "ops@example.com"},
	}
}

func (c *smtpCapture) serve(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	reply := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	reply("220 test.local ESMTP")

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			// 只报 SIZE,不报 STARTTLS:StartTLS 用例靠这一点判 fail-closed。
			reply("250-test.local")
			reply("250 SIZE 10485760")
		case strings.HasPrefix(upper, "HELO"):
			reply("250 test.local")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			c.mu.Lock()
			c.mailFrom = cmd
			c.mu.Unlock()
			reply("250 2.1.0 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			to := strings.TrimSpace(cmd[len("RCPT TO:"):])
			c.mu.Lock()
			c.rcpts = append(c.rcpts, to)
			c.mu.Unlock()
			reply("250 2.1.5 OK")
		case upper == "DATA":
			reply("354 End data with <CR><LF>.<CR><LF>")
			data, ok := readData(br)
			if !ok {
				return
			}
			c.mu.Lock()
			c.data = data
			c.mu.Unlock()
			reply("250 2.0.0 OK")
		case upper == "QUIT":
			reply("221 2.0.0 Bye")
			return
		default:
			reply("502 5.5.2 command not implemented")
		}
	}
}

// readData 读到单独的 "." 行;行首 ".." 是点填充,还原成一个 "."。
// 行尾统一成 \n,交给 net/mail 解析。
func readData(br *bufio.Reader) (string, bool) {
	var b strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return "", false
		}
		text := strings.TrimRight(line, "\r\n")
		if text == "." {
			return b.String(), true
		}
		b.WriteString(strings.TrimPrefix(text, "."))
		b.WriteString("\n")
	}
}

// snapshot 取一份一致的抓取结果。
func (c *smtpCapture) snapshot() (mailFrom, data string, rcpts []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mailFrom, c.data, append([]string(nil), c.rcpts...)
}

func TestSender_DeliversSubjectAndBody(t *testing.T) {
	c := startSMTP(t)
	s := &mailer.Sender{Config: c.config()}

	subject := "[Daedalus] lab-a 巡检：新增 1 · 恶化 0 · 恢复 0"
	body := "新增 1\n- disk-health [critical] /tmp 已写满\n  Filesystem  Size\n  /dev/vdb1   100G"
	if err := s.Send(context.Background(), subject, body); err != nil {
		t.Fatalf("Send 返回错误: %v", err)
	}

	mailFrom, data, rcpts := c.snapshot()
	if data == "" {
		t.Fatal("假服务器没收到正文")
	}
	if !strings.Contains(strings.ToUpper(mailFrom), "PATROL@LAB-A.EXAMPLE") {
		t.Errorf("MAIL FROM 应带 From,实际 %q", mailFrom)
	}
	for _, want := range c.config().To {
		found := false
		for _, got := range rcpts {
			if strings.Contains(got, "<"+want+">") {
				found = true
			}
		}
		if !found {
			t.Errorf("收件人 %q 未出现在 RCPT TO 里,实际 %v", want, rcpts)
		}
	}

	msg, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatalf("抓到的报文解析失败: %v\n%s", err, data)
	}
	gotSubject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("解主题失败: %v", err)
	}
	if gotSubject != subject {
		t.Errorf("主题 = %q,期望 %q", gotSubject, subject)
	}

	// 报文末尾的换行是 SMTP 的 <CRLF>.<CRLF> 终止符,不属于正文。
	gotBody := strings.TrimSuffix(readAll(t, msg.Body), "\n")
	if gotBody != body {
		t.Errorf("正文 = %q,期望 %q", gotBody, body)
	}
}

// readAll 读完消息正文。
func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读正文失败: %v", err)
	}
	return string(b)
}

func TestSender_RequiresStartTLSWhenConfigured(t *testing.T) {
	c := startSMTP(t)
	cfg := c.config()
	cfg.StartTLS = true
	s := &mailer.Sender{Config: cfg}

	err := s.Send(context.Background(), "主题", "正文")
	if err == nil {
		t.Fatal("服务器不支持 STARTTLS 时 Send 应报错,不能明文降级")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "STARTTLS") {
		t.Errorf("错误应点明 STARTTLS,实际 %v", err)
	}
	if _, data, _ := c.snapshot(); data != "" {
		t.Error("拒绝 STARTTLS 后不该把正文发出去")
	}
}

func TestSender_ConnectionErrorNotSwallowed(t *testing.T) {
	// 先占一个端口再关掉,得到一个必然拒绝连接的地址。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占端口失败: %v", err)
	}
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	_ = ln.Close()

	s := &mailer.Sender{Config: mailer.Config{
		Host: host, Port: port,
		From: "patrol@lab-a.example", To: []string{"oncall@example.com"},
	}}
	if err := s.Send(context.Background(), "主题", "正文"); err == nil {
		t.Fatal("连不上 SMTP 时 Send 应返回 error")
	}
}

func TestSender_RejectsIncompleteConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  mailer.Config
	}{
		{"缺 host", mailer.Config{Port: 25, From: "a@b.c", To: []string{"d@e.f"}}},
		{"缺 from", mailer.Config{Host: "127.0.0.1", Port: 25, To: []string{"d@e.f"}}},
		{"缺 to", mailer.Config{Host: "127.0.0.1", Port: 25, From: "a@b.c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &mailer.Sender{Config: tc.cfg}
			if err := s.Send(context.Background(), "主题", "正文"); err == nil {
				t.Fatal("配置不全时 Send 应返回 error,不能静默投递")
			}
		})
	}
}

func TestSender_HonorsCanceledContext(t *testing.T) {
	c := startSMTP(t)
	s := &mailer.Sender{Config: c.config()}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Send(ctx, "主题", "正文")
	if err == nil {
		t.Fatal("ctx 已取消时 Send 应返回 error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("错误应可判为 context.Canceled,实际 %v", err)
	}
}
