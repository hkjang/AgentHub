// Package runtimeenv carries the platform-wide runtime environment: the
// configuration files every Agent Pod is provisioned with and the environment
// variables every container in it exports.
//
// It lives apart from the API and the operator because both ends have to agree
// on exactly what an administrator may declare: the API rejects what the
// operator would have to drop, and the operator drops anything that reached the
// CRD another way.
//
// The canonical case is /etc/pip.conf on an offline site — every runtime needs
// the internal index, and none of them should have to be told about it one agent
// at a time.
package runtimeenv

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

// SettingKey is the system_settings row these settings are stored under.
const SettingKey = "runtimeEnvironment"

// Limits are sized so the rendered ConfigMap stays well inside Kubernetes' 1 MiB
// object limit even with the platform's own configuration alongside it.
const (
	MaxFiles         = 32
	MaxFileBytes     = 64 * 1024
	MaxTotalBytes    = 384 * 1024
	MaxPathLength    = 512
	MaxVariables     = 64
	MaxValueLength   = 4096
	MaxDescription   = 200
	DefaultFileMode  = int32(0o644)
	defaultModeText  = "0644"
	maxFileModeValue = 0o777
)

// File is one configuration file provisioned into every runtime container at
// Path. Content is delivered through a ConfigMap, so it must not carry secrets.
type File struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	Mode        string `json:"mode"`
	Description string `json:"description"`
	// Enabled left unset means enabled: a file declared through the API without
	// the field should be provisioned rather than silently ignored.
	Enabled *bool `json:"enabled,omitempty"`
}

// Variable is one environment variable exported by every container in a runtime
// Pod. It is not a secret either — it is readable on the AgentRuntime object.
type Variable struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

// Settings is the whole runtimeEnvironment setting.
type Settings struct {
	Files     []File     `json:"files"`
	Variables []Variable `json:"variables"`
}

func active(enabled *bool) bool { return enabled == nil || *enabled }

// Active reports whether this file is provisioned.
func (f File) Active() bool { return active(f.Enabled) }

// Active reports whether this variable is exported.
func (v Variable) Active() bool { return active(v.Enabled) }

// FileMode parses the declared permission bits. An empty mode means 0644, which
// is what a configuration file read by everything in the Pod wants.
func (f File) FileMode() (int32, error) {
	text := strings.TrimSpace(f.Mode)
	if text == "" {
		return DefaultFileMode, nil
	}
	value, err := strconv.ParseInt(text, 8, 32)
	if err != nil || value <= 0 || value > maxFileModeValue {
		return 0, fmt.Errorf("파일 권한 %q이 올바르지 않습니다 (예: 0644)", f.Mode)
	}
	return int32(value), nil
}

// reservedPathPrefixes are owned by the platform or by the kernel. Provisioning
// a file into one of them would shadow the generated runtime configuration, a
// platform binary or an adapter's own installation.
var reservedPathPrefixes = []string{"/etc/agenthub", "/proc", "/sys", "/dev", "/run", "/var/run", "/usr/local/bin", "/opt/hermes", "/opt/qwenpaw", "/opt/agenthub"}

// reservedMountPaths are the volumes a runtime Pod already mounts. A file
// provisioned at exactly one of these would be a second mount on the same path,
// which the kubelet rejects — the whole Pod, not just the file. Paths *inside*
// them are allowed: ~/.condarc is a reasonable thing to want.
var reservedMountPaths = []string{"/workspace", "/home/agent", "/tmp"}

// reservedVariablePrefixes and reservedVariableNames are the environment the
// platform itself sets. An administrator who could overwrite these could
// redirect a runtime's model binding, its token or its interpreter.
var (
	reservedVariablePrefixes = []string{"AGENTHUB_", "OPENAI_", "OPENCODE_", "HERMES_", "QWENPAW_", "API_SERVER_", "MCP_", "KUBERNETES_", "LD_"}
	reservedVariableNames    = map[string]bool{"HOME": true, "PATH": true, "OLLAMA_HOST": true, "MODEL": true}
)

// ReservedPath reports whether the platform owns this location.
func ReservedPath(target string) bool {
	clean := path.Clean(target)
	for _, prefix := range reservedPathPrefixes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

// ReservedVariable reports whether the platform owns this variable name.
func ReservedVariable(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	if reservedVariableNames[upper] {
		return true
	}
	for _, prefix := range reservedVariablePrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// ValidatePath checks one target location. It is exported so the API can explain
// a single bad row rather than a whole rejected document.
func ValidatePath(target string) error {
	trimmed := strings.TrimSpace(target)
	switch {
	case trimmed == "":
		return errors.New("파일 경로를 입력해 주세요")
	case len(trimmed) > MaxPathLength:
		return fmt.Errorf("파일 경로는 %d자 이하여야 합니다", MaxPathLength)
	case !strings.HasPrefix(trimmed, "/"):
		return fmt.Errorf("파일 경로 %q는 절대경로여야 합니다", trimmed)
	case strings.HasSuffix(trimmed, "/"):
		return fmt.Errorf("파일 경로 %q는 디렉터리가 아니라 파일이어야 합니다", trimmed)
	case path.Clean(trimmed) != trimmed:
		return fmt.Errorf("파일 경로 %q를 정규화된 경로로 입력해 주세요 (예: %s)", trimmed, path.Clean(trimmed))
	case path.Clean(trimmed) == "/":
		return errors.New("루트 디렉터리에는 파일을 배치할 수 없습니다")
	case ReservedPath(trimmed):
		return fmt.Errorf("%s 경로는 플랫폼이 사용하므로 지정할 수 없습니다", trimmed)
	}
	for _, mount := range reservedMountPaths {
		if path.Clean(trimmed) == mount {
			return fmt.Errorf("경로 %s: Runtime이 마운트하는 디렉터리이므로 그 아래 파일 경로를 지정해 주세요", trimmed)
		}
	}
	return nil
}

// ValidateVariableName checks one variable name.
func ValidateVariableName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return errors.New("환경변수 이름을 입력해 주세요")
	}
	for i, r := range trimmed {
		valid := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')
		if !valid {
			return fmt.Errorf("환경변수 이름 %q는 영문자와 밑줄로 시작하고 영문자·숫자·밑줄만 사용할 수 있습니다", trimmed)
		}
	}
	if ReservedVariable(trimmed) {
		return fmt.Errorf("환경변수 %s: 플랫폼이 사용하므로 지정할 수 없습니다", trimmed)
	}
	return nil
}

// Validate rejects everything the operator would otherwise have to drop, so an
// administrator learns about it while saving rather than by wondering why a Pod
// does not have the file.
func (s Settings) Validate() error {
	if len(s.Files) > MaxFiles {
		return fmt.Errorf("공통 파일은 최대 %d개까지 등록할 수 있습니다", MaxFiles)
	}
	if len(s.Variables) > MaxVariables {
		return fmt.Errorf("공통 환경변수는 최대 %d개까지 등록할 수 있습니다", MaxVariables)
	}
	total, paths := 0, map[string]bool{}
	for _, file := range s.Files {
		if err := ValidatePath(file.Path); err != nil {
			return err
		}
		target := strings.TrimSpace(file.Path)
		if paths[target] {
			return fmt.Errorf("파일 경로 %q가 중복되었습니다", target)
		}
		paths[target] = true
		if _, err := file.FileMode(); err != nil {
			return err
		}
		if len(file.Content) > MaxFileBytes {
			return fmt.Errorf("%s의 내용은 %dKB 이하여야 합니다", target, MaxFileBytes/1024)
		}
		if len(file.Description) > MaxDescription {
			return fmt.Errorf("%s의 설명은 %d자 이하여야 합니다", target, MaxDescription)
		}
		if file.Active() {
			total += len(file.Content)
		}
	}
	if total > MaxTotalBytes {
		return fmt.Errorf("활성화된 공통 파일의 전체 크기는 %dKB 이하여야 합니다", MaxTotalBytes/1024)
	}
	names := map[string]bool{}
	for _, variable := range s.Variables {
		if err := ValidateVariableName(variable.Name); err != nil {
			return err
		}
		name := strings.TrimSpace(variable.Name)
		if names[name] {
			return fmt.Errorf("환경변수 %s: 두 번 지정되었습니다", name)
		}
		names[name] = true
		if len(variable.Value) > MaxValueLength {
			return fmt.Errorf("%s의 값은 %d자 이하여야 합니다", name, MaxValueLength)
		}
		if len(variable.Description) > MaxDescription {
			return fmt.Errorf("%s의 설명은 %d자 이하여야 합니다", name, MaxDescription)
		}
	}
	return nil
}

// Effective is what actually reaches a runtime: enabled entries only, trimmed,
// with anything the platform owns dropped and the order made deterministic so an
// unchanged setting never rolls a Pod.
//
// It filters rather than fails because it also runs against settings that were
// stored before a name became reserved.
func (s Settings) Effective() ([]File, []Variable) {
	files := make([]File, 0, len(s.Files))
	seenPath := map[string]bool{}
	for _, file := range s.Files {
		target := strings.TrimSpace(file.Path)
		if !file.Active() || ValidatePath(target) != nil || seenPath[target] {
			continue
		}
		if _, err := file.FileMode(); err != nil {
			continue
		}
		if len(file.Content) > MaxFileBytes {
			continue
		}
		seenPath[target] = true
		files = append(files, File{Path: target, Content: file.Content, Mode: modeText(file), Description: strings.TrimSpace(file.Description)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	variables := make([]Variable, 0, len(s.Variables))
	seenName := map[string]bool{}
	for _, variable := range s.Variables {
		name := strings.TrimSpace(variable.Name)
		if !variable.Active() || ValidateVariableName(name) != nil || seenName[name] || len(variable.Value) > MaxValueLength {
			continue
		}
		seenName[name] = true
		variables = append(variables, Variable{Name: name, Value: variable.Value, Description: strings.TrimSpace(variable.Description)})
	}
	sort.Slice(variables, func(i, j int) bool { return variables[i].Name < variables[j].Name })
	return files, variables
}

// modeText normalises the declared mode to four octal digits, so two settings
// that both mean 0644 render the same ConfigMap and do not roll a Pod for
// nothing — and so the value always matches the CRD's mode pattern.
func modeText(file File) string {
	mode, err := file.FileMode()
	if err != nil {
		return defaultModeText
	}
	return fmt.Sprintf("0%03o", mode)
}

// ConfigKey names the ConfigMap entry that carries one file. Keys allow only
// alphanumerics, '-', '_' and '.', so the path is hashed for uniqueness and the
// file name is appended to keep `kubectl describe configmap` readable.
func ConfigKey(target string) string {
	sum := sha256.Sum256([]byte(path.Clean(strings.TrimSpace(target))))
	name := path.Base(path.Clean(strings.TrimSpace(target)))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	readable := strings.Trim(b.String(), "._")
	if len(readable) > 40 {
		readable = readable[:40]
	}
	key := "file-" + hex.EncodeToString(sum[:4]) + "-" + readable
	return strings.TrimSuffix(key, "-")
}
