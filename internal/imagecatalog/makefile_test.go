package imagecatalog_test

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/imagecatalog"
	"github.com/hkjang/AgentHub/internal/runtimetype"
)

func TestMakefileConsumesEveryCatalogImageDynamically(t *testing.T) {
	root := repositoryRoot(t)
	catalog, err := imagecatalog.LoadRepository(root, runtimetype.Supported)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Images) != 14 {
		t.Fatalf("catalog has %d images, want 14", len(catalog.Images))
	}

	makefileBytes, err := os.ReadFile(root + "/Makefile")
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(makefileBytes)
	for _, required := range []string{
		"runtime-images.json",
		"image-%:",
		".images[].id",
		".controlBuildArg",
		".versionFile",
		".dockerfile",
		".buildArgs",
		".buildDependencies",
		".bundleDependencies",
		"gzip -n -9",
	} {
		if !strings.Contains(makefile, required) {
			t.Errorf("Makefile does not consume catalog field or required behavior %q", required)
		}
	}

	for _, image := range catalog.Images {
		explicitTarget := regexp.MustCompile(`(?m)^image-` + regexp.QuoteMeta(image.ID) + `\s*:`)
		if explicitTarget.MatchString(makefile) {
			t.Errorf("Makefile reintroduces hard-coded target image-%s", image.ID)
		}
		for _, duplicated := range []string{image.Image, image.VersionFile} {
			if strings.Contains(makefile, duplicated) {
				t.Errorf("Makefile duplicates catalog value %q", duplicated)
			}
		}
	}

	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed; the Make target itself reports the prerequisite")
	}
	idsOutput := runMake(t, root, "-s", "catalog-image-ids")
	gotIDs := strings.Fields(strings.TrimSpace(idsOutput))
	if len(gotIDs) != len(catalog.Images) {
		t.Fatalf("make listed %d images, want %d: %q", len(gotIDs), len(catalog.Images), idsOutput)
	}
	for index, image := range catalog.Images {
		if gotIDs[index] != image.ID {
			t.Errorf("make image %d = %q, want catalog id %q", index, gotIDs[index], image.ID)
		}
		// The recursive dependency line still executes under -n, but every docker
		// command is printed rather than run. This exercises the generic target.
		runMake(t, root, "-n", "image-"+image.ID)
	}
	runMake(t, root, "-n", "image")
}

func TestMakefileRejectsUnknownCatalogImage(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed; the Make target itself reports the prerequisite")
	}
	root := repositoryRoot(t)
	command := exec.Command("make", "--no-print-directory", "-n", "image-not-in-catalog")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("unknown catalog image was accepted:\n%s", output)
	}
	if !strings.Contains(string(output), "unknown runtime image id: not-in-catalog") {
		t.Fatalf("unexpected unknown-image error:\n%s", output)
	}
}

func runMake(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("make", append([]string{"--no-print-directory"}, arguments...)...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
