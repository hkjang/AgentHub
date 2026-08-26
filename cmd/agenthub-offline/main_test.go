package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/offlinebundle"
)

func TestRunPlanRequiresSelectionAndWritesJSON(t *testing.T) {
	manifestPath := writeManifest(t)
	var output, errors bytes.Buffer
	err := run(t.Context(), []string{"plan", "--manifest", manifestPath, "--no-runtimes"}, &output, &errors)
	if err != nil {
		t.Fatal(err)
	}
	var plan offlinebundle.Plan
	if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
		t.Fatalf("plan output is not JSON: %v\n%s", err, output.String())
	}
	if len(plan.Images) != 1 || plan.Images[0].ID != "control" {
		t.Fatalf("plan images = %#v", plan.Images)
	}

	output.Reset()
	err = run(t.Context(), []string{"plan", "--manifest", manifestPath, "--no-runtimes", "--all-runtimes"}, &output, &errors)
	if err == nil || !strings.Contains(err.Error(), "choose exactly one runtime mode") {
		t.Fatalf("conflicting plan flags error = %v", err)
	}
}

func TestRunRejectsUnknownCommandAndTrailingArguments(t *testing.T) {
	for name, arguments := range map[string][]string{
		"unknown":  {"unknown"},
		"trailing": {"plan", "--no-runtimes", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			var output, errors bytes.Buffer
			if err := run(t.Context(), arguments, &output, &errors); err == nil {
				t.Fatal("run accepted invalid command line")
			}
		})
	}
}

func TestManifestCommandValidatesCatalogBeforeReleaseResolution(t *testing.T) {
	directory := t.TempDir()
	catalogPath := filepath.Join(directory, "runtime-images.json")
	if err := os.WriteFile(catalogPath, []byte(`{"schemaVersion":1,"images":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(directory, "releases.json")
	if err := os.WriteFile(indexPath, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output, errors bytes.Buffer
	err := run(t.Context(), []string{
		"manifest", "--release", "v1.0.0", "--catalog", catalogPath, "--release-index", indexPath,
	}, &output, &errors)
	if err == nil || !strings.Contains(err.Error(), "invalid runtime image catalog") {
		t.Fatalf("manifest catalog validation error = %v", err)
	}
}

func TestReleaseHTTPClientBoundsHandshakeAndHeaderStalls(t *testing.T) {
	client := releaseHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.TLSHandshakeTimeout != 15*time.Second {
		t.Fatalf("TLS handshake timeout = %s", transport.TLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout != 60*time.Second {
		t.Fatalf("response header timeout = %s", transport.ResponseHeaderTimeout)
	}
	if client.Timeout != 0 {
		t.Fatalf("whole archive request timeout = %s, want zero", client.Timeout)
	}
}

func writeManifest(t *testing.T) string {
	t.Helper()
	body := []byte("archive")
	digest := sha256.Sum256(body)
	sha := hex.EncodeToString(digest[:])
	name := "agenthub-v1.0.0.tar.gz"
	manifest := offlinebundle.Manifest{
		SchemaVersion: offlinebundle.SchemaVersion,
		Release:       "v1.0.0",
		Platform:      offlinebundle.Platform{OS: "linux", Architecture: "amd64"},
		Prerequisites: []offlinebundle.Prerequisite{{
			ID: offlinebundle.PostgresPrerequisiteID, Kind: "external-service", Required: true,
			Bundled: false, TestedMajor: "17", DSNEnv: offlinebundle.PostgresDSNEnvironment,
			Requirements: []string{"operate PostgreSQL externally"},
		}},
		Images: []offlinebundle.Image{{
			ID: "control", Image: "agenthub", Tag: "v1.0.0", Required: true,
			Artifact: offlinebundle.Artifact{LogicalName: name, SourceRelease: "v1.0.0", Parts: []offlinebundle.Part{{
				Name: name, URL: "https://github.example/owner/repo/releases/download/v1.0.0/" + name,
				Size: int64(len(body)), SHA256: sha,
			}}},
		}},
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "offline-bundle.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(manifest); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
