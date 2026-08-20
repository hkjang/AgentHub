package operator

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// Every `/usr/local/bin/agenthub-*` the platform runs inside a runtime has to be
// a file that runtime's image ships.
//
// The failure this prevents is the one that got three runtimes released unable
// to start: nothing links what the platform executes to what the Dockerfile
// copies, so a rename, a new runtime or a copied-and-edited adapter produces a
// Pod that comes up and then cannot find its own entrypoint. The paths are read
// out of the adapters and the runtime descriptors, the files are read out of the
// Dockerfiles, and neither side is a list anybody maintains by hand.
func TestEveryCommandExistsInItsImage(t *testing.T) {
	root := repositoryRootFrom(t)
	checked := 0
	for _, runtimeType := range runtimetype.Supported {
		shipped, ok := scriptsShippedBy(t, root, runtimeType)
		if !ok {
			// Runtimes that boot from the shared base image: whatever they run comes
			// from Dockerfile.base, which is covered by the release workflow's own
			// gate rather than by this test.
			continue
		}
		for _, command := range commandsThePlatformRuns(runtimeType) {
			// Scanned inside each argument rather than matched against it. Half of
			// these are shell strings — `sh -ec "…exec /usr/local/bin/x"` — so a
			// check that only looked at arguments starting with the path silently
			// skipped every start command, which is most of what matters here.
			for _, path := range scriptPath.FindAllString(strings.Join(command.argv, "\n"), -1) {
				checked++
				if !shipped[filepath.Base(path)] {
					t.Errorf("%s (%s) runs %s, which Dockerfile.%s does not copy — shipped: %v",
						runtimeType, command.what, path, runtimeType, sortedKeys(shipped))
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no commands were checked; the naming convention has changed")
	}
	t.Logf("checked %d command paths against the images that ship them", checked)
}

type platformCommand struct {
	what string
	argv []string
}

// commandsThePlatformRuns collects every argv the platform executes in a runtime:
// what the container starts as, what the initialiser runs, and the wrapper each
// execution backend calls.
func commandsThePlatformRuns(runtimeType string) []platformCommand {
	var build adapterBuild
	build.Name = "rt-1"
	build.Value.Runtime.Type = runtimeType

	commands := []platformCommand{}
	adapter := adapterFor(runtimeType)
	argv := adapter.Args
	if adapter.ArgsFor != nil {
		argv = adapter.ArgsFor(build)
	}
	commands = append(commands, platformCommand{"start", append(append([]string{}, adapter.Command...), argv...)})
	if adapter.InitContainers != nil {
		for _, container := range adapter.InitContainers(build) {
			commands = append(commands, platformCommand{"init " + container.Name,
				append(append([]string{}, container.Command...), container.Args...)})
		}
	}
	for _, runner := range []string{runtimetype.RunnerACP, runtimetype.RunnerCLI, runtimetype.RunnerInvestigate} {
		if command := runtimetype.RunnerCommand(runtimeType, runner); len(command) > 0 {
			commands = append(commands, platformCommand{"runner " + runner, command})
		}
	}
	return commands
}

// scriptPath matches one of the platform's scripts wherever it appears — as an
// argv element or inside a shell string.
var scriptPath = regexp.MustCompile(`/usr/local/bin/agenthub-[a-z0-9-]+`)

// copyLine matches the COPY that installs one of the platform's scripts.
var copyLine = regexp.MustCompile(`(?m)^COPY\s+\S+\s+(/usr/local/bin/agenthub-\S+)\s*$`)

// fromLine matches this image's base, so an image built on another one inherits
// the scripts that one ships — JupyterLab runs Qwen Code's agent and its
// initialiser, and neither is copied by Dockerfile.jupyter.
var fromLine = regexp.MustCompile(`(?m)^FROM\s+\$\{(\w+)_IMAGE[^}]*\}`)

func scriptsShippedBy(t *testing.T, root, runtimeType string) (map[string]bool, bool) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, "Dockerfile."+runtimeType))
	if err != nil {
		return nil, false
	}
	shipped := map[string]bool{}
	for _, match := range copyLine.FindAllStringSubmatch(string(content), -1) {
		shipped[filepath.Base(match[1])] = true
	}
	if parent := fromLine.FindStringSubmatch(string(content)); parent != nil {
		inherited, ok := scriptsShippedBy(t, root, strings.ToLower(parent[1]))
		if !ok {
			t.Fatalf("Dockerfile.%s builds on %s, which has no Dockerfile", runtimeType, parent[1])
		}
		for name := range inherited {
			shipped[name] = true
		}
	}
	return shipped, true
}

func sortedKeys(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func repositoryRootFrom(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for depth := 0; depth < 8; depth++ {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		directory = filepath.Dir(directory)
	}
	t.Fatal("could not find the repository root")
	return ""
}
