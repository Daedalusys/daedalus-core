package mailer_test

// 本文件用一个进程内的最小 SMTP 服务钉住投递面:主题/正文到得到、每个收件人都被
// 单独 RCPT、连不上与 STARTTLS/认证失败都如实报错。不连真实 SMTP、不出本机。

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/mailer"
	"github.com/Daedalusys/daedalus-core/internal/patrol"
)

// 跨包契约:巡检侧只认这一个投递接口。
var _ patrol.Sender = (*mailer.Sender)(nil)

// captured 是一封被收下的信。
type captured struct {
	from  string
	rcpts []string
	data  string // 已还原 dot-stuffing,行尾仍是 CRLF
}

// fakeSMTP 是最小 SMTP 会话:EHLO/MAIL/RCPT/DATA/QUIT,外加可选的 AUTH PLAIN。
// 不声明 STARTTLS —— 需要它的用例正好靠「服务器没提供」来验证拒绝明文投递。
type fakeSMTP struct {
	port int
	msgs chan captured

	auth string // 非空形如 "user:pass",声明并校验 AUTH PLAIN

	mu       sync.Mutex
	authSeen string
	authFail int
}

func newFakeSMTP(t *testing.T, auth string) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起进程内 SMTP 失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	s := &fakeSMTP{
		port: ln.Addr().(*net.TCPAddr).Port,
		msgs: make(chan captured, 4),
		auth: auth,
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	return s
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	br := bufio.NewReader(conn)
	reply := func(format string, args ...any) { _, _ = fmt.Fprintf(conn, format+"\r\n", args...) }
	reply("220 fake ESMTP ready")

	var msg captured
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"):
			caps := []string{"8BITMIME"}
			if s.auth != "" {
				caps = append(caps, "AUTH PLAIN")
			}
			for i, c := range caps {
				if i == len(caps)-1 {
					reply("250 %s", c)
				} else {
					reply("250-%s", c)
				}
			}
		case strings.HasPrefix(verb, "HELO"):
			reply("250 fake")
		case strings.HasPrefix(verb, "AUTH PLAIN"):
			token := strings.TrimSpace(line[len("AUTH PLAIN"):])
			if !s.checkAuth(token) {
				reply("535 5.7.8 认证失败")
				continue
			}
			reply("235 2.7.0 认证通过")
		case strings.HasPrefix(verb, "MAIL FROM:"):
			msg.from = addrOf(line)
			reply("250 2.1.0 OK")
		case strings.HasPrefix(verb, "RCPT TO:"):
			msg.rcpts = append(msg.rcpts, addrOf(line))
			reply("250 2.1.5 OK")
		case verb == "DATA":
			reply("354 以 <CRLF>.<CRLF> 结束")
			data, ok := readData(br)
			if !ok {
				return
			}
			msg.data = data
			s.msgs <- msg
			reply("250 2.0.0 queued")
		case verb == "RSET":
			msg = captured{}
			reply("250 2.0.0 OK")
		case verb == "QUIT":
			reply("221 2.0.0 bye")
			return
		default:
			reply("502 5.5.1 不支持的动词")
		}
	}
}

// checkAuth 解 AUTH PLAIN 的 base64 载荷(authzid\0user\0pass)并与期望比对。
func (s *fakeSMTP) checkAuth(token string) bool {
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		s.recordAuth("", false)
		return false
	}
	parts := strings.Split(string(raw), "\x00")
	got := ""
	if len(parts) == 3 {
		got = parts[1] + ":" + parts[2]
	}
	ok := got == s.auth
	s.recordAuth(got, ok)
	return ok
}

func (s *fakeSMTP) recordAuth(got string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authSeen = got
	if !ok {
		s.authFail++
	}
}

func (s *fakeSMTP) seenAuth() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authSeen
}

// readData 读到单独的 "." 行为止,顺带还原 dot-stuffing。
func readData(br *bufio.Reader) (string, bool) {
	var b strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return "", false
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "." {
			return b.String(), true
		}
		if strings.HasPrefix(line, ".") {
			line = line[1:]
		}
		b.WriteString(line)
		b.WriteString("\r\n")
	}
}

func addrOf(line string) string {
	if i := strings.LastIndex(line, "<"); i >= 0 {
		if j := strings.Index(line[i:], ">"); j > 0 {
			return line[i+1 : i+j]
		}
	}
	return ""
}

func (s *fakeSMTP) wait(t *testing.T) captured {
	t.Helper()
	select {
	case m := <-s.msgs:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("等邮件超时,SMTP 会话没收到 DATA")
		return captured{}
	}
}

// splitMessage 拆信封与正文。Subject 可能是 RFC 2047 编码字,调 decodeHeader 还原。
func splitMessage(t *testing.T, data string) (headers map[string]string, body string) {
	t.Helper()
	lines := strings.Split(data, "\r\n")
	headers = make(map[string]string)
	last := ""
	i := 0
	for ; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			i++
			break
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if last != "" {
				headers[last] += " " + strings.TrimSpace(line)
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("邮件头不是 key: value 形状: %q", line)
		}
		last = strings.ToLower(strings.TrimSpace(key))
		headers[last] = strings.TrimSpace(value)
	}
	body = strings.ReplaceAll(strings.Join(lines[i:], "\r\n"), "\r\n", "\n")
	return headers, body
}

func decodeHeader(t *testing.T, value string) string {
	t.Helper()
	decoded, err := new(mime.WordDecoder).DecodeHeader(value)
	if err != nil {
		t.Fatalf("解码邮件头失败: %v", err)
	}
	return decoded
}

func newSender(t *testing.T, srv *fakeSMTP) *mailer.Sender {
	t.Helper()
	return &mailer.Sender{Config: mailer.Config{
		Host: "127.0.0.1",
		Port: srv.port,
		From: "daedalus@example.com",
		To:   []string{"ops@example.com", "oncall@example.com"},
	}}
}

// subjectAndBody 用真实的巡检渲染走一遍,顺带证明正文里的中文与多行证据到得了收信人。
func subjectAndBody() (string, string) {
	ch := patrol.Changes{Added: []patrol.Finding{{
		Node: "lab-a", ItemID: "disk-health", Status: patrol.StatusProblem,
		Severity: patrol.SeverityWarning,
		Summary:  "/dev/vda1 已用 97%",
		Evidence: []patrol.Evidence{{
			Tool:   "shell_exec",
			Input:  `{"command":"df","args":["-h"]}`,
			Output: "/dev/vda1 40G 40G 0 100% /\n",
		}},
	}}}
	return patrol.FormatMail("lab-a", ch)
}

func TestSender_Send(t *testing.T) {
	srv := newFakeSMTP(t, "")
	subject, body := subjectAndBody()

	if err := newSender(t, srv).Send(context.Background(), subject, body); err != nil {
		t.Fatalf("投递失败: %v", err)
	}

	msg := srv.wait(t)
	if msg.from != "daedalus@example.com" {
		t.Errorf("MAIL FROM = %q, 期望 daedalus@example.com", msg.from)
	}
	wantRcpts := []string{"ops@example.com", "oncall@example.com"}
	if strings.Join(msg.rcpts, ",") != strings.Join(wantRcpts, ",") {
		t.Errorf("RCPT TO = %v, 期望 %v(每个收件人都要单独 RCPT)", msg.rcpts, wantRcpts)
	}

	headers, gotBody := splitMessage(t, msg.data)
	if got := decodeHeader(t, headers["subject"]); got != subject {
		t.Errorf("主题 = %q, 期望 %q", got, subject)
	}
	if gotBody != body {
		t.Errorf("正文与发送内容不一致:\n收到:\n%s\n期望:\n%s", gotBody, body)
	}
	// 证据原文必须逐字到得了收信人手里。
	if !strings.Contains(gotBody, "/dev/vda1 40G 40G 0 100% /\n") {
		t.Errorf("正文里找不到证据原文:\n%s", gotBody)
	}
	if ct := headers["content-type"]; !strings.Contains(ct, "text/plain") || !strings.Contains(ct, "utf-8") {
		t.Errorf("Content-Type = %q, 期望 text/plain; charset=utf-8", ct)
	}
}

func TestSender_Send_DialFailure(t *testing.T) {
	// 先占一个端口再放掉:得到一个几乎必然没人监听的本机端口。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	s := &mailer.Sender{Config: mailer.Config{
		Host: "127.0.0.1", Port: port,
		From: "daedalus@example.com", To: []string{"ops@example.com"},
	}}
	if err := s.Send(context.Background(), "主题", "正文"); err == nil {
		t.Fatal("连不上 SMTP 时必须返回错误,不能吞掉")
	}
}

func TestSender_Send_StartTLSMissing(t *testing.T) {
	srv := newFakeSMTP(t, "")
	s := newSender(t, srv)
	s.Config.StartTLS = true

	err := s.Send(context.Background(), "主题", "正文")
	if err == nil {
		t.Fatal("服务器没提供 STARTTLS 却要求加密投递时必须报错")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("错误里要说清是 STARTTLS 缺失: %v", err)
	}
}

func TestSender_Send_Auth(t *testing.T) {
	srv := newFakeSMTP(t, "patrol:secret")
	s := newSender(t, srv)
	s.Config.Username = "patrol"
	s.Config.Password = "secret"

	if err := s.Send(context.Background(), "主题", "正文"); err != nil {
		t.Fatalf("带认证投递失败: %v", err)
	}
	if got := srv.seenAuth(); got != "patrol:secret" {
		t.Fatalf("服务器收到的凭据 = %q, 期望 patrol:secret", got)
	}
	srv.wait(t)
}

func TestSender_Send_AuthRejected(t *testing.T) {
	srv := newFakeSMTP(t, "patrol:secret")
	s := newSender(t, srv)
	s.Config.Username = "patrol"
	s.Config.Password = "wrong"

	if err := s.Send(context.Background(), "主题", "正文"); err == nil {
		t.Fatal("认证被拒时必须报错")
	}
}

func TestSender_Send_ConfigValidation(t *testing.T) {
	base := mailer.Config{
		Host: "127.0.0.1", Port: 25,
		From: "daedalus@example.com", To: []string{"ops@example.com"},
	}
	cases := map[string]func(*mailer.Config){
		"缺 host": func(c *mailer.Config) { c.Host = "" },
		"缺 from": func(c *mailer.Config) { c.From = "" },
		"没有收件人":  func(c *mailer.Config) { c.To = nil },
		"收件人是空串": func(c *mailer.Config) { c.To = []string{" "} },
		"端口超出范围": func(c *mailer.Config) { c.Port = 70000 },
		"端口是负数":  func(c *mailer.Config) { c.Port = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base
			cfg.To = append([]string(nil), base.To...)
			mutate(&cfg)
			s := &mailer.Sender{Config: cfg}
			if err := s.Send(context.Background(), "主题", "正文"); err == nil {
				t.Fatal("坏配置必须在连接前就报错")
			}
		})
	}
}

// TestSender_Send_ContextCanceled 钉住「轮次超时能把投递拽回来」:会话撒谎收下连接
// 却不回 220,若 Send 不认 ctx 就会一直挂到测试超时。
func TestSender_Send_ContextCanceled(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起静默监听失败: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// 什么都不发,阻塞在读上直到对端放弃。
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 512)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	s := &mailer.Sender{Config: mailer.Config{
		Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port,
		From: "daedalus@example.com", To: []string{"ops@example.com"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = s.Send(ctx, "主题", "正文")
	if err == nil {
		t.Fatal("ctx 到期时必须返回错误")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		// 连接被 ctx 关掉时底层错误是 "use of closed network connection";
		// 只要不是卡死,错误形状不是本测试的断言目标。
		t.Logf("投递错误形状: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("ctx 到期后仍挂了 %v", elapsed)
	}
}
