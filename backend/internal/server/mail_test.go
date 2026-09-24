package server

import (
	"bufio"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

func TestMailLoopback(t *testing.T) {
	cases := map[string]bool{
		"localhost":        true,
		"LOCALHOST":        true,
		"127.0.0.1":        true,
		"::1":              true,
		"mail.example.com": false,
		"203.0.113.5":      false, // TEST-NET-3, definitely not loopback
		"10.0.0.5":         false, // private, but not loopback - still crosses a network
	}
	for host, want := range cases {
		if got := mailLoopback(host); got != want {
			t.Errorf("mailLoopback(%q) = %v, want %v", host, got, want)
		}
	}
}

// TestRequireSTARTTLS is the actual security property this fix adds: sendMail must refuse to proceed in
// clear text with anything but a loopback host, regardless of what the operator typed as --smtp-host.
func TestRequireSTARTTLS(t *testing.T) {
	if err := requireSTARTTLS("mail.example.com", true); err != nil {
		t.Errorf("STARTTLS offered but refused: %v", err)
	}
	if err := requireSTARTTLS("localhost", false); err != nil {
		t.Errorf("loopback without STARTTLS refused: %v", err)
	}
	if err := requireSTARTTLS("127.0.0.1", false); err != nil {
		t.Errorf("loopback IP without STARTTLS refused: %v", err)
	}
	if err := requireSTARTTLS("mail.example.com", false); err == nil {
		t.Error("a remote host that never offered STARTTLS was accepted - this is the downgrade the fix closes")
	}
}

// fakeSMTPServer is a minimal SMTP responder, just enough for net/smtp.Client to talk to: EHLO (optionally
// advertising AUTH PLAIN, never STARTTLS - a real server stripping that line is exactly the attack sendMail
// must not tolerate off of loopback), AUTH PLAIN, MAIL/RCPT/DATA/QUIT. It records the delivered message and
// runs only on 127.0.0.1, so every test using it is exercising sendMail's loopback-exempt path specifically.
type fakeSMTPServer struct {
	ln        net.Listener
	advertise []string // extra EHLO extension lines, e.g. "AUTH PLAIN"
	got       chan string
}

func startFakeSMTP(t *testing.T, advertise ...string) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSMTPServer{ln: ln, advertise: advertise, got: make(chan string, 1)}
	t.Cleanup(func() { _ = ln.Close() })
	go s.serveOne(t)
	return s
}

func (s *fakeSMTPServer) addr() string { return s.ln.Addr().String() }

func (s *fakeSMTPServer) serveOne(t *testing.T) {
	conn, err := s.ln.Accept()
	if err != nil {
		return // listener closed by test cleanup
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	write := func(line string) {
		if _, err := conn.Write([]byte(line + "\r\n")); err != nil {
			t.Logf("fake smtp write: %v", err)
		}
	}
	readLine := func() string {
		line, _ := r.ReadString('\n')
		return strings.TrimRight(line, "\r\n")
	}

	write("220 fake.test ESMTP")
	if !strings.HasPrefix(strings.ToUpper(readLine()), "EHLO") {
		return
	}
	lines := append([]string{"fake.test greets you"}, s.advertise...)
	for i, l := range lines {
		if i == len(lines)-1 {
			write("250 " + l)
		} else {
			write("250-" + l)
		}
	}

	var body strings.Builder
	for {
		line := readLine()
		switch {
		case strings.HasPrefix(strings.ToUpper(line), "AUTH PLAIN"):
			write("235 authenticated")
		case strings.HasPrefix(strings.ToUpper(line), "MAIL FROM"):
			write("250 OK")
		case strings.HasPrefix(strings.ToUpper(line), "RCPT TO"):
			write("250 OK")
		case strings.HasPrefix(strings.ToUpper(line), "DATA"):
			write("354 send it")
			for {
				dl, _ := r.ReadString('\n')
				if dl == ".\r\n" || dl == "." {
					write("250 queued")
					break
				}
				body.WriteString(dl)
			}
			s.got <- body.String()
		case strings.HasPrefix(strings.ToUpper(line), "QUIT"):
			write("221 bye")
			return
		case line == "":
			return
		}
	}
}

func TestSendMailOverLoopbackWithoutSTARTTLS(t *testing.T) {
	s := startFakeSMTP(t) // advertises nothing beyond the greeting - no STARTTLS, no AUTH
	err := sendMail(s.addr(), nil, "continuum@example.com", []string{"person@example.com"}, mimeMessage("continuum@example.com", "person@example.com", "Your code", "123456"))
	if err != nil {
		t.Fatalf("sendMail to a loopback server with no STARTTLS: %v", err)
	}
	select {
	case body := <-s.got:
		if !strings.Contains(body, "123456") {
			t.Errorf("delivered body missing the code: %q", body)
		}
	case <-time.After(time.Second):
		t.Fatal("message was never delivered to the fake server")
	}
}

func TestSendMailAuthenticatesWhenOffered(t *testing.T) {
	s := startFakeSMTP(t, "AUTH PLAIN")
	auth := smtp.PlainAuth("", "user", "pass", "127.0.0.1")
	if err := sendMail(s.addr(), auth, "continuum@example.com", []string{"person@example.com"}, mimeMessage("continuum@example.com", "person@example.com", "Your code", "654321")); err != nil {
		t.Fatalf("sendMail with AUTH offered: %v", err)
	}
	select {
	case <-s.got:
	case <-time.After(time.Second):
		t.Fatal("message was never delivered to the fake server")
	}
}
