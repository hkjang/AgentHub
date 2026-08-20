// Package runtimecfg carries the settings an administrator wants every runtime of
// one type to start with: its language, its default behaviour, whatever knob that
// product happens to expose.
//
// The platform already generates each runtime's own configuration — the model
// binding, the MCP servers, the terminal's working directory — and each runtime
// reads that file from its own home directory. Anything else a site needed had
// nowhere to go: mounting a second copy of the same file would fight the
// generated one, and a per-agent instruction in a prompt does not change what the
// runtime does when a person drives it.
//
// So these are overlays, merged into the configuration the platform writes rather
// than delivered beside it. What lands is reported back from inside the Pod on
// every start, because a setting that silently did not apply is worse than one
// that was never offered: the operator believes their fleet is configured.
package runtimecfg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// SettingKey is the system_settings row these settings live in.
const SettingKey = "runtimeSettings"

// Limits keep one overlay from filling a ConfigMap or a CRD object.
const (
	MaxProfiles      = 8
	MaxEnvPerProfile = 32
	MaxConfigBytes   = 32 * 1024
	MaxValueLength   = 2048
	MaxKeyLength     = 200
)

// Profile is the overlay for one runtime type.
type Profile struct {
	RuntimeType string `json:"runtimeType"`
	// Config is merged into the configuration file the platform generates for
	// this runtime. Nested objects merge key by key, so setting one field does not
	// erase the rest of its section.
	Config map[string]any `json:"config,omitempty"`
	// Env is exported by every container of the runtime. It is here as well as in
	// the platform-wide runtime environment because these are per-runtime-type:
	// LANG for one adapter's terminal is not necessarily right for another's.
	Env map[string]string `json:"env,omitempty"`
	// Description is the operator's note to the next operator.
	Description string `json:"description,omitempty"`
	// Enabled left unset means enabled, so a profile written through the API
	// without the field applies rather than silently doing nothing.
	Enabled *bool `json:"enabled,omitempty"`
}

// Active reports whether this profile is applied.
func (p Profile) Active() bool { return p.Enabled == nil || *p.Enabled }

// Settings is the whole runtimeSettings document.
type Settings struct {
	Profiles []Profile `json:"profiles"`
}

// For returns the active profile for one runtime type, or the zero profile.
func (s Settings) For(runtimeType string) Profile {
	for _, profile := range s.Profiles {
		if profile.RuntimeType == runtimeType && profile.Active() {
			return profile
		}
	}
	return Profile{}
}

// Empty reports whether a profile would change anything.
func (p Profile) Empty() bool { return len(p.Config) == 0 && len(p.Env) == 0 }

// reservedEnv are the variables the platform sets itself. Overwriting one would
// break the runtime in a way that looks like the platform is broken, so they are
// refused at the edge rather than dropped later.
var reservedEnv = []string{
	"AGENTHUB_", "OPENAI_API_KEY", "OPENAI_BASE_URL", "API_SERVER_KEY", "API_SERVER_PORT",
	"API_SERVER_HOST", "API_SERVER_ENABLED", "OPENCODE_SERVER_PASSWORD", "OPENCODE_CONFIG_DIR",
	"HERMES_HOME", "QWENPAW_HOME", "PATH", "HOME",
	// Langflow is configured entirely through the environment, so the platform's
	// share of that environment has to be named rather than prefixed: an
	// administrator may well want LANGFLOW_LOG_LEVEL or LANGFLOW_WORKERS. These
	// particular ones decide where Langflow listens, whether its API asks for a
	// credential, where its database lives and whether it reports usage outside —
	// all of which the platform answers for.
	"LANGFLOW_HOST", "LANGFLOW_PORT", "LANGFLOW_AUTO_LOGIN", "LANGFLOW_API_KEY",
	"LANGFLOW_API_KEY_SOURCE", "LANGFLOW_SKIP_AUTH_AUTO_LOGIN", "LANGFLOW_CONFIG_DIR",
	"LANGFLOW_SAVE_DB_IN_CONFIG_DIR", "LANGFLOW_VARIABLES_TO_GET_FROM_ENVIRONMENT",
	"DO_NOT_TRACK",
}

// configless are the runtimes the platform does not generate a configuration
// file for. An overlay's config block has nowhere to land in them, so it is
// refused at the edge; dropping it silently is the failure this package exists
// to prevent.
var configless = map[string]string{
	runtimetype.Langflow: "Langflow는 설정 파일이 아니라 환경변수로 설정하는 런타임입니다. config 대신 env 항목을 사용해 주세요",
	runtimetype.Custom:   "Custom 런타임의 설정 파일은 플랫폼이 만들지 않습니다. config 대신 env 항목을 사용해 주세요",
}

// reservedConfig are the keys the platform owns in each runtime's configuration.
// An overlay that set them would silently break the model binding or the tool
// policy — the two things a site is most likely to think it configured correctly.
var reservedConfig = map[string][]string{
	runtimetype.OpenCode: {"mcp", "provider", "model"},
	runtimetype.Hermes:   {"mcp_servers", "model"},
	runtimetype.QwenPaw:  {"providers", "active_model"},
	runtimetype.QwenCode: {"mcpServers", "model"},
	runtimetype.Goose:    {"extensions", "GOOSE_PROVIDER", "GOOSE_MODEL"},
	runtimetype.Holmes:   {"mcp_servers", "model", "api_base"},
	// instructions is reserved for a reason the others do not have: it names the
	// file telling the agent how to reach the browser running beside it, and an
	// overlay that replaced it would leave a runtime that starts cleanly and fails
	// every browser task.
	runtimetype.BrowserCode: {"mcp", "provider", "model", "instructions"},
}

// Validate rejects an overlay the platform would have to drop or that would break
// the runtime it is meant to configure.
func (s Settings) Validate() error {
	if len(s.Profiles) > MaxProfiles {
		return fmt.Errorf("런타임 설정 프로파일은 최대 %d개까지 등록할 수 있습니다", MaxProfiles)
	}
	seen := map[string]bool{}
	for _, profile := range s.Profiles {
		if !runtimetype.IsSupported(profile.RuntimeType) {
			return fmt.Errorf("지원하지 않는 Runtime 유형입니다: %s", profile.RuntimeType)
		}
		if seen[profile.RuntimeType] {
			return fmt.Errorf("%s 프로파일이 중복됩니다", profile.RuntimeType)
		}
		seen[profile.RuntimeType] = true
		if len(profile.Description) > 300 {
			return errors.New("설명은 300자 이하여야 합니다")
		}
		if err := validateEnv(profile.Env); err != nil {
			return fmt.Errorf("%s: %w", profile.RuntimeType, err)
		}
		if err := validateConfig(profile.RuntimeType, profile.Config); err != nil {
			return fmt.Errorf("%s: %w", profile.RuntimeType, err)
		}
	}
	return nil
}

func validateEnv(env map[string]string) error {
	if len(env) > MaxEnvPerProfile {
		return fmt.Errorf("환경변수는 프로파일당 최대 %d개입니다", MaxEnvPerProfile)
	}
	for name, value := range env {
		if strings.TrimSpace(name) == "" {
			return errors.New("환경변수 이름을 입력해 주세요")
		}
		if !validEnvName(name) {
			return fmt.Errorf("환경변수 이름은 대문자·숫자·밑줄만 사용할 수 있습니다: %s", name)
		}
		for _, reserved := range reservedEnv {
			if name == reserved || (strings.HasSuffix(reserved, "_") && strings.HasPrefix(name, reserved)) {
				return fmt.Errorf("%s 는 플랫폼이 설정하는 변수라 덮어쓸 수 없습니다", name)
			}
		}
		if len(value) > MaxValueLength {
			return fmt.Errorf("%s 값은 %d자 이하여야 합니다", name, MaxValueLength)
		}
	}
	return nil
}

func validEnvName(name string) bool {
	for index, char := range name {
		switch {
		case char >= 'A' && char <= 'Z', char == '_':
		case char >= '0' && char <= '9' && index > 0:
		default:
			return false
		}
	}
	return name != ""
}

func validateConfig(runtimeType string, config map[string]any) error {
	if len(config) == 0 {
		return nil
	}
	if reason, found := configless[runtimeType]; found {
		return errors.New(reason)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return errors.New("설정을 JSON으로 표현할 수 없습니다")
	}
	if len(encoded) > MaxConfigBytes {
		return fmt.Errorf("설정 문서는 %dKB 이하여야 합니다", MaxConfigBytes/1024)
	}
	for _, key := range reservedConfig[runtimeType] {
		if _, found := config[key]; found {
			return fmt.Errorf("%s 키는 플랫폼이 생성하므로 덮어쓸 수 없습니다", key)
		}
	}
	for key := range config {
		if len(key) > MaxKeyLength {
			return fmt.Errorf("설정 키가 너무 깁니다: %s", key[:40])
		}
	}
	return nil
}

// Merge applies an overlay to the configuration the platform generated.
//
// Objects merge key by key so that setting one field of a section leaves the rest
// of it alone; anything else — a string, a number, an array — replaces what was
// there, because a site that writes a list means that list. The platform's own
// keys are never overwritten: they are what the runtime needs to reach its model
// and its tools, and an overlay that broke them would look like a platform fault.
func Merge(runtimeType string, generated map[string]any, overlay map[string]any) (map[string]any, []string) {
	if generated == nil {
		generated = map[string]any{}
	}
	protected := map[string]bool{}
	for _, key := range reservedConfig[runtimeType] {
		protected[key] = true
	}
	applied := []string{}
	for key, value := range overlay {
		if protected[key] {
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			if existing, ok := generated[key].(map[string]any); ok {
				merged, paths := Merge("", existing, nested)
				generated[key] = merged
				for _, path := range paths {
					applied = append(applied, key+"."+path)
				}
				continue
			}
		}
		generated[key] = value
		applied = append(applied, key)
	}
	sort.Strings(applied)
	return generated, applied
}

// Fingerprint identifies one resolved overlay, so a report can say which version
// of the settings a Pod actually started with.
//
// Values are included: two overlays that differ only in the value of LANG are
// different settings, and an operator asking "did my change reach the fleet"
// needs the answer to change when the value does.
func (p Profile) Fingerprint() string {
	config, _ := json.Marshal(p.Config)
	names := make([]string, 0, len(p.Env))
	for name := range p.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.Write(config)
	for _, name := range names {
		b.WriteString("\x00" + name + "=" + p.Env[name])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:16]
}

// Keys lists what an overlay sets, for a report and for the console. Values are
// deliberately absent: an overlay may carry an internal endpoint or a licence
// string, and neither belongs in a status record.
func (p Profile) Keys() []string {
	keys := make([]string, 0, len(p.Config)+len(p.Env))
	for key := range p.Config {
		keys = append(keys, "config:"+key)
	}
	for name := range p.Env {
		keys = append(keys, "env:"+name)
	}
	sort.Strings(keys)
	return keys
}
