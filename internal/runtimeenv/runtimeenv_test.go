package runtimeenv

import (
	"strings"
	"testing"
)

func TestValidateAcceptsAPipConfiguration(t *testing.T) {
	settings := Settings{
		Files:     []File{{Path: "/etc/pip.conf", Content: "[global]\nindex-url = https://nexus.company.local/repository/pypi/simple\n"}},
		Variables: []Variable{{Name: "PIP_INDEX_URL", Value: "https://nexus.company.local/repository/pypi/simple"}},
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("a plain pip configuration was rejected: %v", err)
	}
	files, variables := settings.Effective()
	if len(files) != 1 || files[0].Mode != "0644" {
		t.Fatalf("unexpected files: %#v", files)
	}
	if len(variables) != 1 || variables[0].Name != "PIP_INDEX_URL" {
		t.Fatalf("unexpected variables: %#v", variables)
	}
}

func TestOmittedEnabledFlagMeansProvisioned(t *testing.T) {
	// A file declared through the API without the field must be provisioned:
	// storing a file that silently does nothing is the worse failure.
	settings := Settings{Files: []File{{Path: "/etc/pip.conf", Content: "x"}}, Variables: []Variable{{Name: "PIP_NO_CACHE_DIR", Value: "1"}}}
	files, variables := settings.Effective()
	if len(files) != 1 || len(variables) != 1 {
		t.Fatalf("an entry without an enabled flag was dropped: %#v %#v", files, variables)
	}
	off := false
	settings.Files[0].Enabled = &off
	settings.Variables[0].Enabled = &off
	files, variables = settings.Effective()
	if len(files) != 0 || len(variables) != 0 {
		t.Fatalf("a disabled entry was provisioned: %#v %#v", files, variables)
	}
}

func TestValidateRejectsPathsThePlatformOwns(t *testing.T) {
	for _, target := range []string{"", "/workspace", "/home/agent", "/tmp", "etc/pip.conf", "/etc/../etc/pip.conf", "/etc/pip.conf/", "/", "/etc/agenthub/runtime.json", "/proc/self/environ", "/usr/local/bin/agenthub-runtime", "/opt/hermes/.venv/bin/hermes"} {
		if err := (Settings{Files: []File{{Path: target}}}).Validate(); err == nil {
			t.Fatalf("path %q was accepted", target)
		}
	}
	for _, target := range []string{"/etc/pip.conf", "/etc/npmrc", "/home/agent/.condarc", "/opt/conda/.condarc"} {
		if err := (Settings{Files: []File{{Path: target}}}).Validate(); err != nil {
			t.Fatalf("path %q was rejected: %v", target, err)
		}
	}
}

func TestValidateRejectsPlatformOwnedVariables(t *testing.T) {
	for _, name := range []string{"", "1PIP", "pip-index", "HOME", "PATH", "OPENAI_API_KEY", "AGENTHUB_RUNTIME_TOKEN", "api_server_key", "LD_PRELOAD"} {
		if err := (Settings{Variables: []Variable{{Name: name}}}).Validate(); err == nil {
			t.Fatalf("variable %q was accepted", name)
		}
	}
	for _, name := range []string{"PIP_INDEX_URL", "HTTPS_PROXY", "NO_PROXY", "_private", "CONDA_CHANNELS"} {
		if err := (Settings{Variables: []Variable{{Name: name}}}).Validate(); err != nil {
			t.Fatalf("variable %q was rejected: %v", name, err)
		}
	}
}

func TestValidateRejectsDuplicatesAndOversizedContent(t *testing.T) {
	duplicate := Settings{Files: []File{{Path: "/etc/pip.conf"}, {Path: "/etc/pip.conf"}}}
	if err := duplicate.Validate(); err == nil {
		t.Fatal("a duplicate path was accepted")
	}
	if err := (Settings{Variables: []Variable{{Name: "HTTP_PROXY"}, {Name: "HTTP_PROXY"}}}).Validate(); err == nil {
		t.Fatal("a duplicate variable was accepted")
	}
	oversized := Settings{Files: []File{{Path: "/etc/pip.conf", Content: strings.Repeat("x", MaxFileBytes+1)}}}
	if err := oversized.Validate(); err == nil {
		t.Fatal("an oversized file was accepted")
	}
	// The rendered ConfigMap has to stay inside Kubernetes' object limit even
	// when every individual file is legal on its own.
	many := Settings{}
	for i := 0; i < MaxFiles; i++ {
		many.Files = append(many.Files, File{Path: "/etc/conf.d/" + string(rune('a'+i)), Content: strings.Repeat("x", MaxFileBytes)})
	}
	if err := many.Validate(); err == nil {
		t.Fatal("a set of files exceeding the total budget was accepted")
	}
}

func TestFileModeParsesOctalAndRejectsNonsense(t *testing.T) {
	for text, expected := range map[string]int32{"": 0o644, "0644": 0o644, "600": 0o600, "0444": 0o444, "0755": 0o755} {
		mode, err := File{Mode: text}.FileMode()
		if err != nil || mode != expected {
			t.Fatalf("mode %q parsed as %o (%v)", text, mode, err)
		}
	}
	for _, text := range []string{"rw-r--r--", "0999", "-1", "0", "07777"} {
		if _, err := (File{Mode: text}).FileMode(); err == nil {
			t.Fatalf("mode %q was accepted", text)
		}
	}
}

func TestEffectiveIsDeterministicAndDropsReservedEntries(t *testing.T) {
	settings := Settings{
		Files:     []File{{Path: "/etc/npmrc", Content: "b"}, {Path: " /etc/pip.conf ", Content: "a", Mode: "644"}, {Path: "/etc/agenthub/runtime.json", Content: "hijack"}},
		Variables: []Variable{{Name: "PIP_INDEX_URL", Value: "b"}, {Name: "HTTPS_PROXY", Value: "a"}, {Name: "PATH", Value: "/hijack"}},
	}
	files, variables := settings.Effective()
	if len(files) != 2 || files[0].Path != "/etc/npmrc" || files[1].Path != "/etc/pip.conf" || files[1].Mode != "0644" {
		t.Fatalf("unexpected files: %#v", files)
	}
	if len(variables) != 2 || variables[0].Name != "HTTPS_PROXY" || variables[1].Name != "PIP_INDEX_URL" {
		t.Fatalf("unexpected variables: %#v", variables)
	}
}

func TestConfigKeyIsAValidAndDistinctConfigMapKey(t *testing.T) {
	// /etc/pip.conf and /etc-pip.conf sanitise to the same text, so the key has
	// to distinguish them or one file would silently overwrite the other.
	if ConfigKey("/etc/pip.conf") == ConfigKey("/etc-pip.conf") {
		t.Fatal("two different paths share a ConfigMap key")
	}
	if ConfigKey("/etc/pip.conf") != ConfigKey("/etc/./pip.conf") {
		t.Fatal("the same path produced two ConfigMap keys")
	}
	for _, target := range []string{"/etc/pip.conf", "/home/agent/.condarc", "/etc/한글.conf", "/etc/" + strings.Repeat("long", 40) + ".conf"} {
		key := ConfigKey(target)
		if len(key) == 0 || len(key) > 253 {
			t.Fatalf("key %q for %q has an unusable length", key, target)
		}
		for _, r := range key {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
			if !ok {
				t.Fatalf("key %q for %q is not a valid ConfigMap key", key, target)
			}
		}
	}
}

func TestEffectiveModeIsAlwaysFourOctalDigits(t *testing.T) {
	// The CRD accepts three octal digits with an optional leading zero, so a mode
	// like "7" must not render as "07" and fail admission on the way out.
	for text, expected := range map[string]string{"": "0644", "7": "0007", "644": "0644", "0755": "0755"} {
		files, _ := Settings{Files: []File{{Path: "/etc/pip.conf", Mode: text}}}.Effective()
		if len(files) != 1 || files[0].Mode != expected {
			t.Fatalf("mode %q rendered as %#v, want %q", text, files, expected)
		}
	}
}
