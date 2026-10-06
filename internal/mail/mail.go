// Package mail sends the transactional email Aster needs, such as verification and
// password reset links.
package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// Message is one plain-text email.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Mailer delivers a Message. Implementations must not log the body, which holds
// one-time tokens.
type Mailer interface {
	Send(ctx context.Context, message Message) error
}

// TLSMode selects how the SMTP connection is secured.
type TLSMode string

const (
	// TLSStartTLS upgrades a plain connection (usually port 587). It is the default.
	TLSStartTLS TLSMode = "starttls"
	// TLSImplicit starts TLS immediately (usually port 465).
	TLSImplicit TLSMode = "tls"
	// TLSNone sends in the clear; only for local development servers.
	TLSNone TLSMode = "none"
)

// SMTPConfig configures SMTPMailer.
type SMTPConfig struct {
	Addr     string
	Username string
	Password string
	From     string
	TLS      TLSMode
}

type SMTPMailer struct {
	config  SMTPConfig
	from    mail.Address
	timeout time.Duration
	// tlsConfig is replaced in tests that use a self-signed certificate.
	tlsConfig *tls.Config
}

func NewSMTPMailer(config SMTPConfig) (*SMTPMailer, error) {
	host, _, err := net.SplitHostPort(config.Addr)
	if err != nil || host == "" {
		return nil, errors.New("smtp address must be host:port")
	}
	from, err := mail.ParseAddress(config.From)
	if err != nil {
		return nil, errors.New("smtp from address is invalid")
	}
	switch config.TLS {
	case "":
		config.TLS = TLSStartTLS
	case TLSStartTLS, TLSImplicit, TLSNone:
	default:
		return nil, fmt.Errorf("smtp tls mode %q must be starttls, tls or none", config.TLS)
	}
	if (config.Username == "") != (config.Password == "") {
		return nil, errors.New("smtp username and password must be set together")
	}
	return &SMTPMailer{config: config, from: *from, timeout: 15 * time.Second, tlsConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}, nil
}

func (m *SMTPMailer) Send(ctx context.Context, message Message) error {
	to, err := mail.ParseAddress(message.To)
	if err != nil {
		return fmt.Errorf("recipient address: %w", err)
	}
	if strings.ContainsAny(message.Subject, "\r\n") {
		return errors.New("subject must be a single line")
	}
	dialer := &net.Dialer{Timeout: m.timeout}
	connection, err := dialer.DialContext(ctx, "tcp", m.config.Addr)
	if err != nil {
		return fmt.Errorf("connect to smtp server: %w", err)
	}
	deadline := time.Now().Add(m.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	if m.config.TLS == TLSImplicit {
		connection = tls.Client(connection, m.tlsConfig)
	}
	host, _, _ := net.SplitHostPort(m.config.Addr)
	client, err := smtp.NewClient(connection, host)
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("start smtp session: %w", err)
	}
	defer client.Close()
	if m.config.TLS == TLSStartTLS {
		if err := client.StartTLS(m.tlsConfig); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if m.config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", m.config.Username, m.config.Password, host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(m.from.Address); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := writer.Write(m.format(to, message)); err != nil {
		return fmt.Errorf("write smtp message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish smtp message: %w", err)
	}
	return client.Quit()
}

// format builds an RFC 5322 message. Addresses come from validated parsing and the
// subject is rejected if multi-line, so no header can be injected.
func (m *SMTPMailer) format(to *mail.Address, message Message) []byte {
	var builder strings.Builder
	builder.WriteString("From: " + m.from.String() + "\r\n")
	builder.WriteString("To: " + (&mail.Address{Address: to.Address}).String() + "\r\n")
	builder.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", message.Subject) + "\r\n")
	builder.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	builder.WriteString("MIME-Version: 1.0\r\n")
	builder.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	builder.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(message.Body, "\r\n", "\n")))
	for len(encoded) > 76 {
		builder.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	builder.WriteString(encoded + "\r\n")
	return []byte(builder.String())
}

// MemoryMailer records Messages for tests.
type MemoryMailer struct {
	mu       sync.Mutex
	messages []Message
	// Err, if set, is returned instead of recording.
	Err error
}

func (m *MemoryMailer) Send(_ context.Context, message Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.messages = append(m.messages, message)
	return nil
}

// Messages returns the Messages sent so far.
func (m *MemoryMailer) Messages() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.messages...)
}
