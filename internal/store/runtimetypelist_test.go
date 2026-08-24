package store

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// The list of runtime types is written in four places: the Go package, the CRD
// enum, and a CHECK constraint on each of two tables. Only the Go package is
// checked by a compiler.
//
// A type added to the platform and not to the others is accepted by the console,
// offered in the catalog, and refused by the database on save — or created and
// then rejected by Kubernetes, which is worse because it happens later and
// somewhere else. This reads all four and compares them.
func TestEveryRuntimeTypeIsAcceptedEverywhereItIsNamed(t *testing.T) {
	supported := map[string]bool{}
	for _, name := range runtimetype.Supported {
		supported[name] = true
	}

	crd, err := os.ReadFile(filepath.Join("..", "..", "deploy", "kubernetes", "crd.yaml"))
	if err != nil {
		t.Skipf("the CRD is not present in this checkout: %v", err)
	}
	enum := regexp.MustCompile(`enum: \[(opencode[^\]]*)\]`).FindStringSubmatch(string(crd))
	if enum == nil {
		t.Fatal("the CRD's runtime type enum is gone; this guard is reading nothing")
	}
	inCRD := map[string]bool{}
	for _, name := range strings.Split(enum[1], ",") {
		inCRD[strings.TrimSpace(name)] = true
	}
	compare(t, "the AgentRuntime CRD", supported, inCRD)

	// The newest migration that rewrites each constraint is what the database
	// ends up with, so that is the one to read.
	for _, table := range []string{"agent_definitions", "agent_templates"} {
		names := latestConstraint(t, table)
		if names == nil {
			t.Errorf("no migration defines %s's runtime type constraint", table)
			continue
		}
		compare(t, table+"'s CHECK constraint", supported, names)
	}
}

func latestConstraint(t *testing.T, table string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	files := []string{}
	for _, entry := range entries {
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	pattern := regexp.MustCompile(`(?s)ADD CONSTRAINT ` + table + `_runtime_type_check\s*\n?\s*CHECK \(runtime_type IN \((.*?)\)\)`)
	var found map[string]bool
	for _, name := range files {
		body, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			continue
		}
		match := pattern.FindStringSubmatch(string(body))
		if match == nil {
			continue
		}
		found = map[string]bool{}
		for _, value := range regexp.MustCompile(`'([a-z0-9]+)'`).FindAllStringSubmatch(match[1], -1) {
			found[value[1]] = true
		}
	}
	return found
}

func compare(t *testing.T, where string, supported, other map[string]bool) {
	t.Helper()
	var missing, extra []string
	for name := range supported {
		if !other[name] {
			missing = append(missing, name)
		}
	}
	for name := range other {
		if !supported[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("%s does not accept %v — this build supports them", where, missing)
	}
	if len(extra) > 0 {
		t.Errorf("%s accepts %v, which this build cannot run", where, extra)
	}
}
