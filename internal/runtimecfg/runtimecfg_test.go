package runtimecfg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

func on() *bool  { value := true; return &value }
func off() *bool { value := false; return &value }

// Merging has to leave the section it did not name alone. A site setting one
// terminal option must not erase the working directory the platform put there.
func TestMergeKeepsWhatItDidNotName(t *testing.T) {
	generated := map[string]any{
		"terminal":    map[string]any{"cwd": "/workspace", "home_mode": "profile"},
		"mcp_servers": map[string]any{"jira": map[string]any{"url": "https://mcp"}},
	}
	merged, applied := Merge(runtimetype.Hermes, generated, map[string]any{
		"terminal": map[string]any{"shell": "/bin/bash"},
		"theme":    "dark",
	})
	terminal := merged["terminal"].(map[string]any)
	if terminal["cwd"] != "/workspace" || terminal["home_mode"] != "profile" {
		t.Fatalf("the generated terminal section was damaged: %#v", terminal)
	}
	if terminal["shell"] != "/bin/bash" || merged["theme"] != "dark" {
		t.Fatalf("the overlay was not applied: %#v", merged)
	}
	if strings.Join(applied, ",") != "terminal.shell,theme" {
		t.Fatalf("applied = %v", applied)
	}
}

// The keys the platform owns are what the runtime needs to reach its model and its
// tools. An overlay that broke them would look like a platform fault.
func TestMergeRefusesPlatformKeys(t *testing.T) {
	generated := map[string]any{"model": "agenthub/qwen", "mcp": map[string]any{"jira": true}}
	merged, applied := Merge(runtimetype.OpenCode, generated, map[string]any{
		"model": "something/else", "mcp": map[string]any{}, "autoupdate": false,
	})
	if merged["model"] != "agenthub/qwen" {
		t.Fatalf("the model binding was overwritten: %#v", merged["model"])
	}
	if _, ok := merged["mcp"].(map[string]any)["jira"]; !ok {
		t.Fatalf("the tool bindings were overwritten: %#v", merged["mcp"])
	}
	if merged["autoupdate"] != false || strings.Join(applied, ",") != "autoupdate" {
		t.Fatalf("the rest of the overlay must still apply: %#v %v", merged, applied)
	}
}

// A value replaces; only objects merge. A site that writes a list means that list.
func TestMergeReplacesScalarsAndLists(t *testing.T) {
	merged, _ := Merge("", map[string]any{"tools": []any{"a", "b"}, "count": 3}, map[string]any{"tools": []any{"c"}, "count": 5})
	if list := merged["tools"].([]any); len(list) != 1 || list[0] != "c" {
		t.Fatalf("a list must be replaced, not appended: %#v", merged["tools"])
	}
	if merged["count"] != 5 {
		t.Fatalf("count = %#v", merged["count"])
	}
}

func TestValidate(t *testing.T) {
	valid := Settings{Profiles: []Profile{{
		RuntimeType: runtimetype.OpenCode, Config: map[string]any{"autoupdate": false},
		Env: map[string]string{"LANG": "ko_KR.UTF-8"},
	}}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a normal profile must be accepted: %v", err)
	}
	cases := []struct {
		name     string
		settings Settings
		mentions string
	}{
		{name: "an unknown runtime would never apply",
			settings: Settings{Profiles: []Profile{{RuntimeType: "codex"}}}, mentions: "지원하지 않는"},
		{name: "two profiles for one runtime is ambiguous",
			settings: Settings{Profiles: []Profile{{RuntimeType: runtimetype.Hermes}, {RuntimeType: runtimetype.Hermes}}}, mentions: "중복"},
		// The platform sets these itself; letting a site overwrite one breaks the
		// runtime in a way that looks like the platform is broken.
		{name: "the platform's own variables are refused",
			settings: Settings{Profiles: []Profile{{RuntimeType: runtimetype.Hermes, Env: map[string]string{"OPENAI_API_KEY": "x"}}}}, mentions: "덮어쓸 수 없습니다"},
		{name: "so is anything in the platform's namespace",
			settings: Settings{Profiles: []Profile{{RuntimeType: runtimetype.Hermes, Env: map[string]string{"AGENTHUB_MODEL_NAME": "x"}}}}, mentions: "덮어쓸 수 없습니다"},
		{name: "a lowercase variable name is not a variable name",
			settings: Settings{Profiles: []Profile{{RuntimeType: runtimetype.Hermes, Env: map[string]string{"lang": "ko"}}}}, mentions: "대문자"},
		{name: "the generated config keys are refused too",
			settings: Settings{Profiles: []Profile{{RuntimeType: runtimetype.OpenCode, Config: map[string]any{"provider": map[string]any{}}}}}, mentions: "덮어쓸 수 없습니다"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := test.settings.Validate()
			if err == nil {
				t.Fatal("the settings must be refused")
			}
			if !strings.Contains(err.Error(), test.mentions) {
				t.Fatalf("the message must mention %q; got %q", test.mentions, err)
			}
		})
	}
}

// "Did my change reach the fleet" is the question these settings exist to answer,
// so the fingerprint has to change when the value does.
func TestFingerprintFollowsTheValues(t *testing.T) {
	korean := Profile{Env: map[string]string{"LANG": "ko_KR.UTF-8"}}
	english := Profile{Env: map[string]string{"LANG": "en_US.UTF-8"}}
	if korean.Fingerprint() == english.Fingerprint() {
		t.Fatal("a different value must produce a different fingerprint")
	}
	if korean.Fingerprint() != (Profile{Env: map[string]string{"LANG": "ko_KR.UTF-8"}}).Fingerprint() {
		t.Fatal("the same settings must produce the same fingerprint")
	}
	// The keys a report carries never include the values: an overlay may hold an
	// internal endpoint or a licence string.
	profile := Profile{Config: map[string]any{"autoupdate": false}, Env: map[string]string{"HTTPS_PROXY": "http://secret.internal:3128"}}
	keys := strings.Join(profile.Keys(), ",")
	if strings.Contains(keys, "secret.internal") {
		t.Fatalf("the reported keys leak a value: %s", keys)
	}
	if keys != "config:autoupdate,env:HTTPS_PROXY" {
		t.Fatalf("keys = %s", keys)
	}
}

func TestProfileSelection(t *testing.T) {
	settings := Settings{Profiles: []Profile{
		{RuntimeType: runtimetype.OpenCode, Env: map[string]string{"TZ": "Asia/Seoul"}, Enabled: on()},
		{RuntimeType: runtimetype.Hermes, Env: map[string]string{"TZ": "UTC"}, Enabled: off()},
	}}
	if settings.For(runtimetype.OpenCode).Env["TZ"] != "Asia/Seoul" {
		t.Fatal("the active profile must be returned")
	}
	if !settings.For(runtimetype.Hermes).Empty() {
		t.Fatal("a disabled profile must not apply")
	}
	if !settings.For(runtimetype.QwenPaw).Empty() {
		t.Fatal("a runtime with no profile must get nothing")
	}
}

// A dotted suggestion has to set one field, not replace the section it lives in.
func TestExpand(t *testing.T) {
	encoded, _ := json.Marshal(Expand("terminal.cwd", "/workspace/app"))
	if string(encoded) != `{"terminal":{"cwd":"/workspace/app"}}` {
		t.Fatalf("Expand = %s", encoded)
	}
	if encoded, _ := json.Marshal(Expand("autoupdate", false)); string(encoded) != `{"autoupdate":false}` {
		t.Fatalf("Expand = %s", encoded)
	}
}

// The catalogue is honest about what it knows: a suggestion with no key is
// guidance, and one with a key names something this platform or the OS defines.
func TestSuggestionsAreHonest(t *testing.T) {
	for _, item := range Suggestions("") {
		if item.Label == "" || item.Description == "" {
			t.Errorf("a suggestion needs a label and a description: %#v", item)
		}
		if item.Verified && item.Key == "" {
			t.Errorf("a verified suggestion must name its key: %#v", item)
		}
		if !item.Verified && item.Key != "" {
			t.Errorf("an unverified suggestion must not pretend to know the key: %#v", item)
		}
		if item.Target != TargetConfig && item.Target != TargetEnv {
			t.Errorf("unknown target: %#v", item)
		}
	}
	// Narrowing by runtime keeps the shared ones and drops the others'.
	hermes := Suggestions(runtimetype.Hermes)
	found := map[string]bool{}
	for _, item := range hermes {
		found[item.Label] = true
	}
	if !found["언어·로케일"] || !found["터미널 시작 디렉터리"] || found["자동 업데이트"] {
		t.Fatalf("Hermes suggestions are wrong: %v", found)
	}
}
