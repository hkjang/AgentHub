package offlinebundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/imagecatalog"
)

func TestBuildManifestIncludesExternalPostgresAndEveryCatalogImage(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "BASE_VERSION"), []byte("1.2.3\n"))
	writeTestFile(t, filepath.Join(root, "JUPYTER_VERSION"), []byte("2.0.0\n"))
	assets := filepath.Join(root, "assets")
	if err := os.Mkdir(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"agenthub-v3.0.0.tar.gz":         "control",
		"agenthub-base-v1.2.3.tar.gz":    "base",
		"agenthub-jupyter-v2.0.0.tar.gz": "jupyter",
	} {
		writeTestFile(t, filepath.Join(assets, name), []byte(body))
	}
	composePath := filepath.Join(root, "compose.yaml")
	writeTestFile(t, composePath, []byte("services: {}\n"))
	catalog := &imagecatalog.Catalog{SchemaVersion: 1, Images: []imagecatalog.Image{
		{ID: "base", Image: "agenthub-base", VersionFile: "BASE_VERSION", RuntimeTypes: []string{"opencode", "hermes"}},
		{ID: "jupyter", Image: "agenthub-jupyter", VersionFile: "JUPYTER_VERSION", RuntimeTypes: []string{"jupyter"}, BuildDependencies: []string{"base"}},
	}}
	manifest, err := BuildManifest(ManifestOptions{
		Release: "v3.0.0", Repository: "owner/repository", Catalog: catalog, CatalogRoot: root, LocalAssets: assets,
		DeploymentFiles: []LocalDeploymentFile{{Name: "agenthub-offline-compose.yaml", Path: composePath}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Images) != 3 || manifest.Images[0].ID != "control" {
		t.Fatalf("images = %#v", manifest.Images)
	}
	if len(manifest.Prerequisites) != 1 {
		t.Fatalf("prerequisites = %#v", manifest.Prerequisites)
	}
	postgres := manifest.Prerequisites[0]
	if postgres.ID != PostgresPrerequisiteID || postgres.Kind != "external-service" || !postgres.Required || postgres.Bundled || postgres.DSNEnv != PostgresDSNEnvironment || postgres.TestedMajor != "17" {
		t.Fatalf("PostgreSQL prerequisite = %#v", postgres)
	}
	for _, image := range manifest.Images {
		if strings.Contains(strings.ToLower(image.Image+image.Artifact.LogicalName), "postgres") {
			t.Fatalf("PostgreSQL leaked into image artifact %#v", image)
		}
	}
	if got := manifest.Images[2].Dependencies; len(got) != 0 {
		t.Fatalf("Jupyter bundle dependencies = %v, want none for a build-only dependency", got)
	}
}

func TestManifestValidationRejectsBundledPostgresAndMixedReleaseURL(t *testing.T) {
	manifest := validTestManifest()
	manifest.Images = append(manifest.Images, Image{
		ID: "postgres", Image: "postgres", Tag: "17", RuntimeTypes: []string{"database"},
		Artifact: testArtifact("agenthub-postgres-v17.0.0.tar.gz", "v1.0.0", "postgres"),
	})
	manifest.Images[0].Artifact.Parts[0].URL = testURL("v0.9.0", manifest.Images[0].Artifact.Parts[0].Name)
	err := manifest.Validate()
	if err == nil {
		t.Fatal("Validate accepted a bundled database and mixed source URL")
	}
	for _, want := range []string{"illegally bundles PostgreSQL", "does not identify"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestManifestValidationBindsNamesReleasesAndBundleGraph(t *testing.T) {
	manifest := validTestManifest()
	manifest.DeploymentFiles = []DeploymentFile{{
		Name: "postgres-image.tar.gz", SourceRelease: "v1.0.0",
		URL: testURL("v1.0.0", "postgres-image.tar.gz"), Size: 1, SHA256: testDigest([]byte("x")),
	}}
	manifest.Images[1].Artifact.LogicalName = "different.tar.gz"
	manifest.Images[1].Artifact.SourceRelease = "v1.0.1"
	manifest.Images[1].Artifact.Parts[0].URL = testURL("v1.0.1", manifest.Images[1].Artifact.Parts[0].Name)
	manifest.Images[2].Dependencies = []string{"jupyter"}
	manifest.Images[3].Dependencies = []string{"qwencode", "qwencode"}

	err := manifest.Validate()
	if err == nil {
		t.Fatal("Validate accepted unrelated artifacts, a future source, and an invalid dependency graph")
	}
	for _, want := range []string{
		"illegally bundles a PostgreSQL asset",
		"artifact.logicalName",
		"is newer than manifest release",
		"dependency \"qwencode\" is duplicated",
		"image bundle dependency cycle",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestDecodeManifestRejectsUnknownAndTrailingJSON(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":  `{"schemaVersion":1,"unexpected":true}`,
		"trailing": `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeManifest(strings.NewReader(body)); err == nil {
				t.Fatal("DecodeManifest accepted invalid JSON")
			}
		})
	}
}

func validTestManifest() *Manifest {
	return &Manifest{
		SchemaVersion: SchemaVersion,
		Release:       "v1.0.0",
		Platform:      Platform{OS: "linux", Architecture: "amd64"},
		Prerequisites: []Prerequisite{{
			ID: PostgresPrerequisiteID, Kind: "external-service", Required: true, Bundled: false,
			TestedMajor: "17", DSNEnv: PostgresDSNEnvironment, Requirements: []string{"operate PostgreSQL externally"},
		}},
		Images: []Image{
			{ID: "control", Image: "agenthub", Tag: "v1.0.0", Required: true, Artifact: testArtifact("agenthub-v1.0.0.tar.gz", "v1.0.0", "control")},
			{ID: "base", Image: "agenthub-base", Tag: "v2.0.0", RuntimeTypes: []string{"opencode", "hermes"}, Artifact: testArtifact("agenthub-base-v2.0.0.tar.gz", "v1.0.0", "base")},
			{ID: "qwencode", Image: "agenthub-qwencode", Tag: "v3.0.0", RuntimeTypes: []string{"qwencode"}, Artifact: testArtifact("agenthub-qwencode-v3.0.0.tar.gz", "v1.0.0", "qwen")},
			{ID: "jupyter", Image: "agenthub-jupyter", Tag: "v4.0.0", RuntimeTypes: []string{"jupyter"}, Dependencies: []string{"qwencode"}, Artifact: testArtifact("agenthub-jupyter-v4.0.0.tar.gz", "v1.0.0", "jupyter")},
		},
	}
}

func testArtifact(name, release, body string) Artifact {
	return Artifact{LogicalName: name, SourceRelease: release, Parts: []Part{{
		Name: name, URL: testURL(release, name), Size: int64(len(body)), SHA256: testDigest([]byte(body)),
	}}}
}
