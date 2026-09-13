package mail

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// SettingKey is the system_settings row these settings live in. The fields
// inside it carry the names every service in the building uses — an operator
// who learned mail.smtp_host once should not learn it again here — so the
// full names read mail.enabled, mail.smtp_host and so on. The password is the
// one field not in the blob: it lives in the row's secret slot, encrypted, and
// the settings API never returns it.
const SettingKey = "mail"

// Security modes. auto follows what the relay advertises, which is what makes
// one set of defaults fit both an internal port-25 relay and a hosted provider.
const (
	SecurityAuto     = "auto"
	SecurityNone     = "none"
	SecuritySTARTTLS = "starttls"
	SecurityTLS      = "tls"
)

// Defaults aim at the common case: an internal relay on port 25 that accepts
// mail from the network without credentials.
const (
	defaultPort    = 25
	defaultTimeout = 10 * time.Second
)

// Events this platform mails about. Each is something a person is waiting on
// — the test is that without the mail somebody loses something or keeps
// refreshing a screen — and each has its own switch, so an administrator can
// silence one kind without silencing the rest.
const (
	// EventApprovalRequested goes to whoever can decide: a task or a tool call
	// is stopped until they do.
	EventApprovalRequested = "approval.requested"
	// EventApprovalDecided goes to whoever asked, who has been waiting.
	EventApprovalDecided = "approval.decided"
	// EventTaskHandoff: a run gave the rest of the work to a person, and the
	// task waits until that person opens the workspace.
	EventTaskHandoff = "task.handoff"
	// EventTaskFailed: a task stopped and will not resume by itself — it
	// failed, ran out of attempts, was refused by policy or by a budget.
	EventTaskFailed = "task.failed"
	// EventDependency goes to administrators when a model endpoint or an MCP
	// server changes state; every task on it is about to fail with a reason
	// that reads like the agent's fault.
	EventDependency = "dependency.changed"
	// EventTest is the administrator's test button.
	EventTest = "test"
)

// Settings is the blob an administrator saves. Field names are the standard's
// keys without the mail. prefix; the settings-key sweep in internal/api checks
// that each one is read.
type Settings struct {
	Enabled       bool   `json:"enabled"`
	SMTPHost      string `json:"smtp_host"`
	SMTPPort      int    `json:"smtp_port"`
	Security      string `json:"security"`
	SkipTLSVerify bool   `json:"skip_tls_verify"`
	// Username is optional: an internal relay usually asks for nothing. The
	// password that goes with it is the row's secret, never a field here.
	Username    string `json:"username"`
	FromAddress string `json:"from_address"`
	FromName    string `json:"from_name"`
	// BaseURL is where the links in a mail point. Empty means the general
	// setting's public URL.
	BaseURL        string `json:"base_url"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	// Per-event switches, on by default. A missing key is on — see Allows.
	NotifyApproval   *bool `json:"notify_approval"`
	NotifyHandoff    *bool `json:"notify_handoff"`
	NotifyTaskFailed *bool `json:"notify_task_failed"`
	NotifyDependency *bool `json:"notify_dependency"`
}

// Defaults are what an unconfigured deployment gets: off, and shaped for an
// internal relay should somebody turn it on.
func Defaults() Settings {
	return Settings{SMTPPort: defaultPort, Security: SecurityAuto, FromName: "AgentHub", TimeoutSeconds: int(defaultTimeout / time.Second)}
}

// Normalized trims what an administrator typed and fills the fields that have
// a fixed set of values.
func (s Settings) Normalized() Settings {
	s.SMTPHost = strings.TrimSpace(s.SMTPHost)
	s.Security = strings.ToLower(strings.TrimSpace(s.Security))
	if s.Security == "" {
		s.Security = SecurityAuto
	}
	if s.SMTPPort == 0 {
		s.SMTPPort = defaultPort
	}
	// A relay on the implicit TLS port needs no extra configuration.
	if s.Security == SecurityAuto && s.SMTPPort == 465 {
		s.Security = SecurityTLS
	}
	s.Username = strings.TrimSpace(s.Username)
	s.FromAddress = strings.TrimSpace(s.FromAddress)
	s.FromName = strings.TrimSpace(s.FromName)
	if s.FromName == "" {
		s.FromName = "AgentHub"
	}
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = int(defaultTimeout / time.Second)
	}
	return s
}

// Validate is called on the way in, so a relay that cannot be reached by these
// settings is refused at the form rather than discovered as a queue of failed
// deliveries. Off, anything goes: the fields are not used.
func (s Settings) Validate() error {
	s = s.Normalized()
	if s.SMTPPort < 1 || s.SMTPPort > 65535 {
		return errors.New("SMTP 포트는 1~65535 사이여야 합니다")
	}
	switch s.Security {
	case SecurityAuto, SecurityNone, SecuritySTARTTLS, SecurityTLS:
	default:
		return errors.New("보안 방식은 auto, none, starttls, tls 중 하나여야 합니다")
	}
	if s.TimeoutSeconds > 120 {
		return errors.New("SMTP 시간 제한은 120초 이하여야 합니다")
	}
	if s.BaseURL != "" {
		parsed, err := url.Parse(s.BaseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("메일 속 링크 주소는 http(s):// 로 시작하는 주소여야 합니다")
		}
	}
	if !s.Enabled {
		return nil
	}
	if s.SMTPHost == "" {
		return errors.New("메일을 켜려면 SMTP 릴레이 주소가 필요합니다")
	}
	if !strings.Contains(s.FromAddress, "@") {
		return errors.New("보내는 주소는 메일 주소여야 합니다")
	}
	return nil
}

// Config turns the saved settings and the separately held password into what
// the transport needs. publicURL is the fallback for links.
func (s Settings) Config(password, publicURL string) Config {
	s = s.Normalized()
	base := s.BaseURL
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	}
	return Config{
		Enabled: s.Enabled, Host: s.SMTPHost, Port: s.SMTPPort, Security: s.Security, SkipVerify: s.SkipTLSVerify,
		Username: s.Username, Password: password, FromAddress: s.FromAddress, FromName: s.FromName,
		BaseURL: base, Timeout: time.Duration(s.TimeoutSeconds) * time.Second,
		Events: map[string]bool{
			EventApprovalRequested: switched(s.NotifyApproval),
			EventApprovalDecided:   switched(s.NotifyApproval),
			EventTaskHandoff:       switched(s.NotifyHandoff),
			EventTaskFailed:        switched(s.NotifyTaskFailed),
			EventDependency:        switched(s.NotifyDependency),
		},
	}
}

// switched reads a per-event switch: absent is on, so a deployment that saved
// its settings before an event existed still hears about it.
func switched(value *bool) bool { return value == nil || *value }

// Link makes a console path absolute for a mail. With no address configured
// the path is sent as written, which at least says where to look.
func (c Config) Link(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if c.BaseURL == "" {
		return path
	}
	return c.BaseURL + "/" + strings.TrimLeft(path, "/")
}

// Describe is the one-line summary the readiness list shows.
func (c Config) Describe() string {
	return fmt.Sprintf("%s · %s · 보내는 사람 %s", c.endpoint(), c.Security, c.FromAddress)
}
