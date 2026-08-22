package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Three things on this platform stop a runtime that nobody asked them to stop:
// the idle culler, the warm pool cooling one down, and a task releasing the
// runtime it started. Each had its own idea of what "in use" means, and each was
// missing a different part of it — the pool would cool a runtime holding a task
// handed to a person, the culler measured a timestamp only a browser wrote, and
// the task release asked who started it rather than who is in it.
//
// They ask one question now. An explicit stop — a person pressing the button, an
// agent calling the runtime action — is obeyed, because "stop this runtime" from
// somebody who means it is not a thing to second-guess; it is recorded instead.
func TestEveryAutomaticStopAsksWhoIsInTheRuntime(t *testing.T) {
	// Where the stop is somebody's decision rather than the platform's.
	explicit := map[string]string{
		"api/routes.go": "the console's stop button: a person pressing it and watching what happens",
		"api/mcp.go":    "the runtime action tool: a caller with runtime:manage asking in so many words",
	}
	stop := regexp.MustCompile(`spawner\.Stop\(`)
	root := filepath.Join("..", "..", "internal")
	sites := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		source := string(body)
		for _, at := range stop.FindAllStringIndex(source, -1) {
			sites++
			key := filepath.ToSlash(path)
			skip := false
			for suffix := range explicit {
				if strings.HasSuffix(key, suffix) {
					skip = true
				}
			}
			if skip {
				continue
			}
			// The question has to be asked before this call, in the same function.
			start := strings.LastIndex(source[:at[0]], "\nfunc ")
			if start < 0 {
				start = 0
			}
			if !strings.Contains(source[start:at[0]], "RuntimeBusy(") {
				t.Errorf("%s stops a runtime without asking whether anybody is in it", key)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sites < 5 {
		t.Fatalf("only %d stop site(s) found; this guard is not reading the tree", sites)
	}
}

// The pool's own query is the cheap pre-filter, and the statuses in it are the
// ones a cooling runtime would interrupt.
func TestCoolingLeavesWorkAlone(t *testing.T) {
	body, err := os.ReadFile("pool.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) RuntimesToCool(")
	if at < 0 {
		t.Fatal("RuntimesToCool is gone; this guard is reading nothing")
	}
	query := source[at:]
	if end := strings.Index(query, "\n}\n"); end >= 0 {
		query = query[:end]
	}
	for _, status := range []string{"queued", "running", "waiting_tool", "retrying", "handoff"} {
		if !strings.Contains(query, "'"+status+"'") {
			t.Errorf("a task in status %s no longer stops the pool cooling its runtime", status)
		}
	}
}
