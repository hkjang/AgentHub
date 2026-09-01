package imagecatalog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/imagecatalog"
	"github.com/hkjang/AgentHub/internal/runtimetype"
)

func TestRepositoryCatalogIsCompleteAndValid(t *testing.T) {
	root := repositoryRoot(t)
	catalog, err := imagecatalog.LoadRepository(root, runtimetype.Supported)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Images) != 14 {
		t.Fatalf("catalog has %d images, want base plus 13 independent images", len(catalog.Images))
	}

	base, found := catalog.ByID("base")
	if !found {
		t.Fatal("base image is absent")
	}
	if got := strings.Join(base.RuntimeTypes, ","); got != "opencode,hermes,qwenpaw" {
		t.Errorf("base runtime types = %s", got)
	}
	for _, source := range []string{"cmd/runtime-proxy", "internal/dlp", "internal/policy"} {
		if !contains(base.SourcePaths, source) {
			t.Errorf("base source paths do not watch %s", source)
		}
	}

	jupyter, found := catalog.ByID("jupyter")
	if !found || len(jupyter.BuildDependencies) != 1 || jupyter.BuildDependencies[0] != "qwencode" {
		t.Errorf("Jupyter build dependency = %v, want qwencode", jupyter.BuildDependencies)
	}
	if len(jupyter.BundleDependencies) != 0 {
		t.Errorf("Jupyter bundle dependencies = %v, want none because its archive contains Qwen Code layers", jupyter.BundleDependencies)
	}
	openHands, found := catalog.ImageForRuntime(runtimetype.OpenHands)
	if !found || openHands.Health.Kind != "http" || openHands.Health.Port != 8000 || openHands.Health.Path != "/health" {
		t.Errorf("OpenHands health = %#v", openHands.Health)
	}
}

func TestDecodeRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field":  `{"schemaVersion":1,"images":[],"image":true}`,
		"trailing value": `{"schemaVersion":1,"images":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := imagecatalog.Decode(strings.NewReader(body)); err == nil {
				t.Fatal("Decode accepted invalid JSON")
			}
		})
	}
}

func TestValidatorRejectsCatalogDrift(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*imagecatalog.Catalog)
		supported []string
		want      string
	}{
		{
			name: "duplicate id",
			mutate: func(catalog *imagecatalog.Catalog) {
				catalog.Images = append(catalog.Images, catalog.Images[0])
			},
			want: "duplicates images[0]",
		},
		{
			name: "postgres image reserved",
			mutate: func(catalog *imagecatalog.Catalog) {
				catalog.Images[0].ID = "postgres"
				catalog.Images[0].Image = "agenthub-postgres"
			},
			want: "PostgreSQL; it is an external service",
		},
		{
			name: "runtime mapped twice",
			mutate: func(catalog *imagecatalog.Catalog) {
				second := catalog.Images[0]
				second.ID, second.Image, second.VersionFile = "beta", "agenthub-beta", "BETA_VERSION"
				second.ControlBuildArg, second.BuildInfoSymbol = "BETA_VERSION", "BetaVersion"
				catalog.Images = append(catalog.Images, second)
			},
			want: "already mapped",
		},
		{
			name:      "supported runtime missing",
			mutate:    func(*imagecatalog.Catalog) {},
			supported: []string{"alpha", "beta", "custom"},
			want:      `supported runtime type "beta" is not mapped`,
		},
		{
			name: "missing source",
			mutate: func(catalog *imagecatalog.Catalog) {
				catalog.Images[0].SourcePaths = append(catalog.Images[0].SourcePaths, "missing.sh")
			},
			want: "does not exist",
		},
		{
			name: "copied source not watched",
			mutate: func(catalog *imagecatalog.Catalog) {
				catalog.Images[0].SourcePaths = []string{"Dockerfile.alpha"}
			},
			want: `does not cover "script.sh"`,
		},
		{
			name: "unknown dependency",
			mutate: func(catalog *imagecatalog.Catalog) {
				catalog.Images[0].BuildDependencies = []string{"missing"}
			},
			want: `names unknown image "missing"`,
		},
		{
			name: "invalid version tag",
			mutate: func(catalog *imagecatalog.Catalog) {
				catalog.Images[0].VersionFile = "BAD_VERSION"
				catalog.Images[0].ControlBuildArg = "BAD_VERSION"
			},
			want: "does not exist",
		},
		{
			name: "build arg drift",
			mutate: func(catalog *imagecatalog.Catalog) {
				catalog.Images[0].BuildArgs["UPSTREAM_VERSION"] = "9.9.9"
			},
			want: "Dockerfile default is",
		},
		{
			name: "mutable upstream",
			mutate: func(catalog *imagecatalog.Catalog) {
				catalog.Images[0].BuildArgs["UPSTREAM_VERSION"] = "latest"
			},
			want: "uses mutable value",
		},
		{
			name: "invalid health",
			mutate: func(catalog *imagecatalog.Catalog) {
				catalog.Images[0].Health = imagecatalog.Health{Kind: "http", Port: 70000, Path: "health"}
			},
			want: "outside 1..65535",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, catalog := fixture(t)
			test.mutate(catalog)
			supported := test.supported
			if supported == nil {
				supported = []string{"alpha", "custom"}
			}
			err := catalog.ValidateRepository(root, supported)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateRepository() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidatorRejectsDependencyCyclesAndStaleTags(t *testing.T) {
	root, catalog := fixture(t)
	write(t, root, "BETA_VERSION", "2.0.0")
	write(t, root, "Dockerfile.beta", "ARG ALPHA_IMAGE=agenthub-alpha:v1.2.3\nFROM ${ALPHA_IMAGE}\nARG UPSTREAM_VERSION=2.0.0\nCOPY script.sh /script.sh\n")
	beta := imagecatalog.Image{
		ID: "beta", Image: "agenthub-beta", VersionFile: "BETA_VERSION", Dockerfile: "Dockerfile.beta",
		RuntimeTypes: []string{"beta"}, SourcePaths: []string{"Dockerfile.beta", "script.sh"},
		ControlBuildArg: "BETA_VERSION", BuildInfoSymbol: "BetaVersion",
		BuildArgs:         map[string]string{"ALPHA_IMAGE": "agenthub-alpha:v1.2.3", "UPSTREAM_VERSION": "2.0.0"},
		BuildDependencies: []string{"alpha"}, Label: "Beta", Note: "Beta runtime.",
		Health: imagecatalog.Health{Kind: "command", Command: []string{"beta", "--version"}},
	}
	catalog.Images = append(catalog.Images, beta)
	catalog.Images[0].BuildDependencies = []string{"beta"}
	catalog.Images[0].BuildArgs["BETA_IMAGE"] = "agenthub-beta:v1.9.9"
	write(t, root, "Dockerfile.alpha", "ARG UPSTREAM_VERSION=1.0.0\nARG BETA_IMAGE=agenthub-beta:v1.9.9\nFROM scratch\nCOPY script.sh /script.sh\n")

	err := catalog.ValidateRepository(root, []string{"alpha", "beta", "custom"})
	if err == nil {
		t.Fatal("dependency cycle was accepted")
	}
	for _, want := range []string{"is not pinned as build arg value", "image build dependency cycle"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestValidatorRejectsInvalidVersionContentsAndUncataloguedFiles(t *testing.T) {
	root, catalog := fixture(t)
	write(t, root, "ALPHA_VERSION", "v1.2.3\n")
	write(t, root, "ORPHAN_VERSION", "1.0.0\n")
	write(t, root, "Dockerfile.orphan", "FROM scratch\n")
	err := catalog.ValidateRepository(root, []string{"alpha", "custom"})
	if err == nil {
		t.Fatal("invalid version and uncatalogued files were accepted")
	}
	for _, want := range []string{
		`contains "v1.2.3", want a plain semantic version`,
		`version file "ORPHAN_VERSION" is not represented`,
		`runtime Dockerfile "Dockerfile.orphan" is not represented`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func fixture(t *testing.T) (string, *imagecatalog.Catalog) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "ALPHA_VERSION", "1.2.3")
	write(t, root, "script.sh", "#!/bin/sh\n")
	write(t, root, "Dockerfile.alpha", "FROM scratch\nARG UPSTREAM_VERSION=1.0.0\nCOPY script.sh /script.sh\n")
	return root, &imagecatalog.Catalog{SchemaVersion: imagecatalog.SchemaVersion, Images: []imagecatalog.Image{{
		ID: "alpha", Image: "agenthub-alpha", VersionFile: "ALPHA_VERSION", Dockerfile: "Dockerfile.alpha",
		RuntimeTypes: []string{"alpha"}, SourcePaths: []string{"Dockerfile.alpha", "script.sh"},
		ControlBuildArg: "ALPHA_VERSION", BuildInfoSymbol: "AlphaVersion",
		BuildArgs: map[string]string{"UPSTREAM_VERSION": "1.0.0"}, BuildDependencies: []string{}, BundleDependencies: []string{},
		Label: "Alpha", Note: "Alpha runtime.", Health: imagecatalog.Health{Kind: "command", Command: []string{"alpha", "--version"}},
	}}}
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for depth := 0; depth < 8; depth++ {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		directory = filepath.Dir(directory)
	}
	t.Fatal("could not find repository root")
	return ""
}
