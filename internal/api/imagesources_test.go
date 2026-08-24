package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A runtime image is only rebuilt when one of the files the release names as its
// sources has changed. A file the Dockerfile copies and that list does not
// mention is a change that ships nothing: the release reuses the previous image,
// says so in its log, and nobody looks again.
//
// That is not hypothetical. orca-agents-configure.sh — the script that points
// codex at this deployment's gateway rather than at a vendor — was copied by the
// Dockerfile and missing from the list. An image without it stalls every worker,
// which is exactly what a stale image did on the cluster while this was found.
func TestEveryImageSourceIsNamedInTheRelease(t *testing.T) {
	root := filepath.Join("..", "..")
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Skipf("the release workflow is not present in this checkout: %v", err)
	}
	entries := regexp.MustCompile(`"([a-z0-9]+):([A-Z_]+_VERSION):([^"]+)"`).FindAllStringSubmatch(string(workflow), -1)
	if len(entries) < 5 {
		t.Fatalf("found only %d runtime images in the release; the pattern this test reads by has probably changed", len(entries))
	}
	copied := regexp.MustCompile(`COPY (deploy/runtime/[A-Za-z0-9._-]+)`)
	for _, entry := range entries {
		name, sources := entry[1], entry[3]
		body, err := os.ReadFile(filepath.Join(root, "Dockerfile."+name))
		if err != nil {
			t.Errorf("%s is released and has no Dockerfile.%s", name, name)
			continue
		}
		listed := map[string]bool{}
		for _, source := range strings.Fields(sources) {
			listed[source] = true
		}
		var missing []string
		for _, match := range copied.FindAllStringSubmatch(string(body), -1) {
			if !listed[match[1]] {
				missing = append(missing, match[1])
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s: the image copies %v, and the release does not watch %v — changing one of them would ship the old image",
				name, "these files", missing)
		}
	}
}
