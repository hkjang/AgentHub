package execution

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Three things start a runtime: a task acquiring one, the warm pool starting one
// ahead of a schedule, and the console doing it on somebody's behalf. All three
// spend the same cluster and are governed by the same two rules — the owner's
// runtime quota, and the platform policy on starting a runtime.
//
// Starting from a task once went around both. That was fixed by asking. The pool
// was the third path and was never asked at all: a person held to three runtimes
// could hold more by scheduling them with a warm-up, and an agent a policy
// forbids anyone from starting was started by its own nightly schedule a minute
// before it ran.
//
// The console's paths ask their own way — they answer a person directly and can
// return the refusal as an HTTP status — so they are named here rather than
// required to use this function.
func TestEveryPathThatStartsARuntimeAsksWhetherItMay(t *testing.T) {
	console := map[string]string{
		"api/routes.go": "the console's start button and its state change: they check the quota inline and answer the person with it",
		"api/mcp.go":    "the runtime action tool, which answers its caller the same way",
	}
	start := regexp.MustCompile(`spawner\.Start\(`)
	sites := 0
	err := filepath.Walk(filepath.Join("..", ".."), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name == "node_modules" || name == ".git" || name == "web" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		source := string(body)
		for _, at := range start.FindAllStringIndex(source, -1) {
			sites++
			key := filepath.ToSlash(path)
			skip := false
			for suffix := range console {
				if strings.HasSuffix(key, suffix) {
					skip = true
				}
			}
			if skip {
				continue
			}
			from := strings.LastIndex(source[:at[0]], "\nfunc ")
			if from < 0 {
				from = 0
			}
			if !strings.Contains(source[from:at[0]], "runtimeStartRefusal(") && !strings.Contains(source[from:at[0]], "runtimeRefusal(") {
				t.Errorf("%s starts a runtime without asking whether the owner's quota and the platform policy allow it", key)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sites < 4 {
		t.Fatalf("only %d start site(s) found; this guard is not reading the tree", sites)
	}
}

// And the two paths the console exempts still have to check the quota, or the
// exemption above becomes a hole rather than a note.
func TestTheConsolePathsStillCheckTheQuota(t *testing.T) {
	for _, file := range []string{"routes.go", "mcp.go"} {
		body, err := os.ReadFile(filepath.Join("..", "api", file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "CheckRuntimeQuota") {
			t.Errorf("api/%s starts a runtime and never checks the runtime quota", file)
		}
	}
}
