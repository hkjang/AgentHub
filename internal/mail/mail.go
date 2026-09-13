// Package mail delivers event notifications through a company SMTP relay.
//
// The platform already tells people things through the bell in the console: a
// task stopped for an approval, a run handed itself back to a person, a model
// endpoint went away. The bell only reaches somebody who has the console open,
// and the events worth a notice are exactly the ones that happen while nobody
// does — a scheduled task fails at two in the morning, an approval waits for a
// reviewer in a meeting. This package carries the same notices out of the
// building over SMTP.
//
// Internal relays commonly accept mail on port 25 with no credentials and no
// TLS, so authentication and encryption are optional and the transport adapts
// to whatever the server advertises. Nothing here blocks a request: an event
// queues a row and returns, a background sender delivers it, and every attempt
// is recorded so an administrator can see what left the building — without the
// body, which would make the record itself a leak.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

var (
	ErrDisabled = errors.New("mail is disabled")
	ErrInvalid  = errors.New("invalid mail configuration")
)

// Config is what the transport needs, read out of the saved settings and the
// separately held password.
type Config struct {
	Enabled     bool
	Host        string
	Port        int
	Username    string
	Password    string
	FromAddress string
	FromName    string
	Security    string
	SkipVerify  bool
	BaseURL     string
	Timeout     time.Duration
	Events      map[string]bool
}

// Address is the RFC 5322 From header value.
func (c Config) Address() string {
	from := strings.TrimSpace(c.FromAddress)
	if name := strings.TrimSpace(c.FromName); name != "" {
		return fmt.Sprintf("%s <%s>", name, from)
	}
	return from
}

func (c Config) endpoint() string { return net.JoinHostPort(c.Host, fmt.Sprint(c.Port)) }

// Allows reports whether an event should be delivered. Unknown events are sent,
// so adding a notification never requires a settings change first.
func (c Config) Allows(event string) bool {
	if enabled, known := c.Events[event]; known {
		return enabled
	}
	return true
}

// Validate is what stands between "enabled" and "sending": a relay with no
// host, a sender with no address. The message names the setting key so the
// delivery record an administrator reads says what to fix.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("%w: mail.smtp_host 가 비어 있습니다", ErrInvalid)
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("%w: mail.smtp_port 는 1~65535 사이여야 합니다", ErrInvalid)
	}
	if !strings.Contains(c.FromAddress, "@") {
		return fmt.Errorf("%w: mail.from_address 는 메일 주소여야 합니다", ErrInvalid)
	}
	switch c.Security {
	case SecurityAuto, SecurityNone, SecuritySTARTTLS, SecurityTLS:
	default:
		return fmt.Errorf("%w: mail.security 는 auto, none, starttls, tls 중 하나여야 합니다", ErrInvalid)
	}
	return nil
}

// Message is one mail on its way to one recipient.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Deliver opens a connection and sends one message. It is exported so the
// settings screen can prove the relay works before anything depends on it.
func Deliver(ctx context.Context, config Config, message Message) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(message.To) == "" {
		return fmt.Errorf("%w: 받는 사람이 비어 있습니다", ErrInvalid)
	}
	client, err := dial(ctx, config)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if err := startSession(client, config); err != nil {
		return err
	}
	if err := client.Mail(strings.TrimSpace(config.FromAddress)); err != nil {
		return fmt.Errorf("MAIL FROM 실패: %w", err)
	}
	if err := client.Rcpt(strings.TrimSpace(message.To)); err != nil {
		return fmt.Errorf("RCPT TO 실패: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA 실패: %w", err)
	}
	if _, err := writer.Write([]byte(compose(config, message))); err != nil {
		return fmt.Errorf("본문 전송 실패: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("본문 종료 실패: %w", err)
	}
	return client.Quit()
}

// Verify performs the handshake without sending anything, which is what the
// readiness list needs: whether the relay answers, upgrades and accepts the
// credentials, without a test mail landing in somebody's inbox every check.
func Verify(ctx context.Context, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	client, err := dial(ctx, config)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if err := startSession(client, config); err != nil {
		return err
	}
	return client.Quit()
}

func dial(ctx context.Context, config Config) (*smtp.Client, error) {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	dialer := &net.Dialer{Timeout: timeout}
	var connection net.Conn
	var err error
	if config.Security == SecurityTLS {
		connection, err = tls.DialWithDialer(dialer, "tcp", config.endpoint(), config.tlsConfig())
		if err != nil {
			return nil, fmt.Errorf("SMTP TLS 연결 실패: %w", err)
		}
	} else {
		connection, err = dialer.DialContext(ctx, "tcp", config.endpoint())
		if err != nil {
			return nil, fmt.Errorf("SMTP 연결 실패: %w", err)
		}
	}
	// The dialer's timeout covers the connect only; a relay that accepts the
	// connection and then says nothing would otherwise hold the sender for ever.
	_ = connection.SetDeadline(time.Now().Add(timeout))
	client, err := smtp.NewClient(connection, config.Host)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("SMTP 세션 시작 실패: %w", err)
	}
	return client, nil
}

// startSession upgrades and authenticates only as far as the relay allows, so
// an unauthenticated internal relay works with the same settings as a hosted
// provider that demands both.
func startSession(client *smtp.Client, config Config) error {
	if err := client.Hello(helloName(config)); err != nil {
		return fmt.Errorf("EHLO 실패: %w", err)
	}
	if config.Security == SecuritySTARTTLS || config.Security == SecurityAuto {
		if supported, _ := client.Extension("STARTTLS"); supported {
			if err := client.StartTLS(config.tlsConfig()); err != nil {
				return fmt.Errorf("STARTTLS 실패: %w", err)
			}
		} else if config.Security == SecuritySTARTTLS {
			return fmt.Errorf("%w: 서버가 STARTTLS 를 지원하지 않습니다", ErrInvalid)
		}
	}
	if strings.TrimSpace(config.Username) == "" {
		return nil
	}
	supported, mechanisms := client.Extension("AUTH")
	if !supported {
		return fmt.Errorf("%w: 서버가 인증을 지원하지 않습니다. mail.username 을 비우고 사용하세요", ErrInvalid)
	}
	upper := strings.ToUpper(mechanisms)
	switch {
	case strings.Contains(upper, "PLAIN"):
		return client.Auth(smtp.PlainAuth("", config.Username, config.Password, config.Host))
	case strings.Contains(upper, "LOGIN"):
		return client.Auth(loginAuth{username: config.Username, password: config.Password, host: config.Host})
	default:
		return client.Auth(smtp.CRAMMD5Auth(config.Username, config.Password))
	}
}

func (c Config) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.SkipVerify} //nolint:gosec // opt-in for internal relays with private certificates
}

// helloName keeps the EHLO name to the sender domain, which relays that check
// the greeting are happier with than a container hostname.
func helloName(config Config) string {
	if index := strings.LastIndex(config.FromAddress, "@"); index >= 0 && index+1 < len(config.FromAddress) {
		return config.FromAddress[index+1:]
	}
	return "localhost"
}

// loginAuth implements the LOGIN mechanism that several corporate relays use
// instead of PLAIN. The standard library only ships PLAIN and CRAM-MD5.
type loginAuth struct{ username, password, host string }

func (a loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && server.Name != a.host {
		return "", nil, errors.New("LOGIN 인증은 신뢰할 수 있는 서버에서만 사용합니다")
	}
	return "LOGIN", nil, nil
}

func (a loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimRight(string(fromServer), ": ")) {
	case "username":
		return []byte(a.username), nil
	case "password":
		return []byte(a.password), nil
	}
	return nil, fmt.Errorf("알 수 없는 LOGIN 요청: %s", fromServer)
}

// compose builds a MIME message. Korean subjects and bodies are encoded so
// relays and clients that predate UTF-8 headers still show them correctly.
func compose(config Config, message Message) string {
	var builder strings.Builder
	builder.WriteString("From: " + encodeAddress(config.Address()) + "\r\n")
	builder.WriteString("To: " + strings.TrimSpace(message.To) + "\r\n")
	builder.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", message.Subject) + "\r\n")
	builder.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	builder.WriteString("MIME-Version: 1.0\r\n")
	builder.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	builder.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	builder.WriteString("Auto-Submitted: auto-generated\r\n")
	builder.WriteString("X-AgentHub-Notification: 1\r\n")
	builder.WriteString("\r\n")
	builder.WriteString(normalizeBody(message.Body))
	return builder.String()
}

func encodeAddress(address string) string {
	open := strings.LastIndex(address, "<")
	if open <= 0 {
		return address
	}
	return mime.QEncoding.Encode("utf-8", strings.TrimSpace(address[:open])) + " " + address[open:]
}

// normalizeBody uses CRLF line endings and escapes a leading dot so a line of
// text can never terminate the DATA command early.
func normalizeBody(body string) string {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	if strings.HasPrefix(body, ".") {
		body = "." + body
	}
	body = strings.ReplaceAll(body, "\r\n.", "\r\n..")
	if !strings.HasSuffix(body, "\r\n") {
		body += "\r\n"
	}
	return body
}
