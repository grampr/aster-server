package mail

import (
	"bufio"
	"context"
	"encoding/base64"
	"mime"
	"net"
	"strings"
	"testing"
)

// smtpSink is a minimal SMTP server that records the first message it receives.
type smtpSink struct {
	listener net.Listener
	got      chan string
	commands chan []string
}

func newSMTPSink(t *testing.T) *smtpSink {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &smtpSink{listener: listener, got: make(chan string, 1), commands: make(chan []string, 1)}
	t.Cleanup(func() { listener.Close() })
	go sink.serve()
	return sink
}

func (s *smtpSink) serve() {
	connection, err := s.listener.Accept()
	if err != nil {
		return
	}
	defer connection.Close()
	reader := bufio.NewReader(connection)
	reply := func(line string) { _, _ = connection.Write([]byte(line + "\r\n")) }
	reply("220 sink ESMTP")
	var commands []string
	var data strings.Builder
	inData := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				s.got <- data.String()
				reply("250 queued")
				continue
			}
			data.WriteString(line + "\n")
			continue
		}
		commands = append(commands, line)
		switch {
		case strings.HasPrefix(line, "EHLO"):
			reply("250 sink")
		case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
			reply("250 ok")
		case line == "DATA":
			inData = true
			reply("354 go ahead")
		case line == "QUIT":
			s.commands <- commands
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func TestSMTPMailerSendsAnEncodedMessage(t *testing.T) {
	sink := newSMTPSink(t)
	mailer, err := NewSMTPMailer(SMTPConfig{Addr: sink.listener.Addr().String(), From: "Aster <no-reply@example.com>", TLS: TLSNone})
	if err != nil {
		t.Fatal(err)
	}
	body := "確認コード: abc\nline two"
	if err := mailer.Send(context.Background(), Message{To: "alice@example.com", Subject: "【Aster】確認", Body: body}); err != nil {
		t.Fatal(err)
	}
	raw := <-sink.got
	headers, encoded, _ := strings.Cut(raw, "\n\n")
	decodedSubject := ""
	for _, line := range strings.Split(headers, "\n") {
		if value, ok := strings.CutPrefix(line, "Subject: "); ok {
			decodedSubject, err = new(mime.WordDecoder).DecodeHeader(value)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if decodedSubject != "【Aster】確認" || !strings.Contains(headers, "To: <alice@example.com>") || !strings.Contains(headers, "From: \"Aster\" <no-reply@example.com>") ||
		!strings.Contains(headers, "Content-Transfer-Encoding: base64") {
		t.Fatalf("unexpected headers: %s", headers)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(encoded), "\n", ""))
	if err != nil || string(decoded) != body {
		t.Fatalf("unexpected body: %q %v", decoded, err)
	}
	commands := <-sink.commands
	if !strings.Contains(strings.Join(commands, "|"), "RCPT TO:<alice@example.com>") {
		t.Fatalf("unexpected SMTP commands: %v", commands)
	}
}

func TestSMTPMailerRejectsHeaderInjection(t *testing.T) {
	mailer, err := NewSMTPMailer(SMTPConfig{Addr: "127.0.0.1:1", From: "no-reply@example.com", TLS: TLSNone})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []Message{
		{To: "alice@example.com", Subject: "hi\r\nBcc: victim@example.com", Body: "x"},
		{To: "alice@example.com\r\nBcc: victim@example.com", Subject: "hi", Body: "x"},
		{To: "not an address", Subject: "hi", Body: "x"},
	} {
		if err := mailer.Send(context.Background(), message); err == nil {
			t.Fatalf("message %+v must be rejected before connecting", message)
		}
	}
}

func TestNewSMTPMailerValidatesConfiguration(t *testing.T) {
	for name, config := range map[string]SMTPConfig{
		"no port":          {Addr: "localhost", From: "a@example.com"},
		"bad from":         {Addr: "localhost:25", From: "nope"},
		"bad tls mode":     {Addr: "localhost:25", From: "a@example.com", TLS: "ssl"},
		"user without pwd": {Addr: "localhost:25", From: "a@example.com", Username: "u"},
	} {
		if _, err := NewSMTPMailer(config); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}
