package offlinebundle

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetcherDownloadsVerifiesAndSkipsExistingFiles(t *testing.T) {
	body := []byte("an offline image archive")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		response.Header().Set("Content-Length", "24")
		_, _ = response.Write(body)
	}))
	defer server.Close()
	plan := testDownloadPlan("agenthub-v1.0.0.tar.gz", server.URL+"/archive", body)
	directory := t.TempDir()
	fetcher := Fetcher{Client: server.Client(), AllowHTTP: true}
	if err := fetcher.Fetch(context.Background(), plan, directory); err != nil {
		t.Fatal(err)
	}
	if err := fetcher.Fetch(context.Background(), plan, directory); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("request count = %d, want one because the verified file is reused", got)
	}
	got, err := os.ReadFile(filepath.Join(directory, "agenthub-v1.0.0.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("downloaded body = %q", got)
	}
}

func TestFetcherCleansPartialFileOnChecksumOrSizeFailure(t *testing.T) {
	body := []byte("downloaded content")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write(body)
	}))
	defer server.Close()
	for name, mutate := range map[string]func(*Plan){
		"checksum": func(plan *Plan) { plan.Images[0].Artifact.Parts[0].SHA256 = testDigest([]byte("different")) },
		"size":     func(plan *Plan) { plan.Images[0].Artifact.Parts[0].Size++ },
	} {
		t.Run(name, func(t *testing.T) {
			plan := testDownloadPlan("agenthub-v1.0.0.tar.gz", server.URL, body)
			mutate(plan)
			directory := t.TempDir()
			err := (Fetcher{Client: server.Client(), AllowHTTP: true}).Fetch(context.Background(), plan, directory)
			if err == nil {
				t.Fatal("Fetch succeeded with bad integrity metadata")
			}
			entries, readErr := os.ReadDir(directory)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("failed fetch left files behind: %v", entries)
			}
		})
	}
}

func TestFetcherReplacesInvalidExistingFileAtomically(t *testing.T) {
	body := []byte("valid")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write(body)
	}))
	defer server.Close()
	directory := t.TempDir()
	name := "agenthub-v1.0.0.tar.gz"
	writeTestFile(t, filepath.Join(directory, name), []byte("wrong"))
	plan := testDownloadPlan(name, server.URL, body)
	if err := (Fetcher{Client: server.Client(), AllowHTTP: true}).Fetch(context.Background(), plan, directory); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("file = %q, want %q", got, body)
	}
}

func TestFetcherRejectsPlainHTTPAndConflictingNames(t *testing.T) {
	body := []byte("body")
	plan := testDownloadPlan("agenthub-v1.0.0.tar.gz", "http://example.invalid/archive", body)
	if err := (Fetcher{}).Fetch(context.Background(), plan, t.TempDir()); err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("Fetch() error = %v", err)
	}
	plan.DeploymentFiles = []DeploymentFile{{
		Name: "agenthub-v1.0.0.tar.gz", URL: "https://example.invalid/other", Size: int64(len(body)), SHA256: testDigest([]byte("other")),
	}}
	if err := (Fetcher{}).Fetch(context.Background(), plan, t.TempDir()); err == nil || !strings.Contains(err.Error(), "conflicting metadata") {
		t.Fatalf("Fetch() conflict error = %v", err)
	}
}

func TestFetcherRejectsHTTPSRedirectToHTTP(t *testing.T) {
	body := []byte("body")
	plain := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write(body)
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, plain.URL+"/archive", http.StatusFound)
	}))
	defer secure.Close()
	plan := testDownloadPlan("agenthub-v1.0.0.tar.gz", secure.URL+"/start", body)
	err := (Fetcher{Client: secure.Client()}).Fetch(context.Background(), plan, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "redirected final URL must use HTTPS") {
		t.Fatalf("Fetch() error = %v", err)
	}
}

func TestLoaderStreamsSplitPartsWithoutReassembly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	directory := t.TempDir()
	first, second := []byte("first-"), []byte("second")
	firstName, secondName := "agenthub-v1.0.0.tar.gz.part-aa", "agenthub-v1.0.0.tar.gz.part-ab"
	writeTestFile(t, filepath.Join(directory, firstName), first)
	writeTestFile(t, filepath.Join(directory, secondName), second)
	capture := filepath.Join(directory, "captured")
	script := filepath.Join(directory, "fake-docker")
	writeTestFile(t, script, []byte("#!/bin/sh\ncase \"$1\" in\n  load) cat > \"$CAPTURE_PATH\" ;;\n  image) [ \"$2\" = inspect ] && [ \"$5\" = agenthub:v1.0.0 ] || exit 9; echo sha256:loaded ;;\n  *) exit 9 ;;\nesac\n"))
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE_PATH", capture)
	plan := &Plan{Images: []Image{{
		ID: "control", Image: "agenthub", Tag: "v1.0.0",
		Artifact: Artifact{LogicalName: "agenthub-v1.0.0.tar.gz", Parts: []Part{
			{Name: firstName, Size: int64(len(first)), SHA256: testDigest(first)},
			{Name: secondName, Size: int64(len(second)), SHA256: testDigest(second)},
		}},
	}}}
	if err := (Loader{DockerBinary: script}).Load(context.Background(), plan, directory); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if want := append(append([]byte(nil), first...), second...); !bytes.Equal(got, want) {
		t.Fatalf("docker stdin = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(directory, "agenthub-v1.0.0.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("loader created a reassembled archive: %v", err)
	}
}

func TestLoaderRejectsCorruptPartBeforeRunningDocker(t *testing.T) {
	directory := t.TempDir()
	name := "agenthub-v1.0.0.tar.gz"
	writeTestFile(t, filepath.Join(directory, name), []byte("corrupt"))
	plan := testDownloadPlan(name, "https://example.invalid/archive", []byte("expected"))
	err := (Loader{DockerBinary: filepath.Join(directory, "does-not-exist")}).Load(context.Background(), plan, directory)
	if err == nil || !strings.Contains(err.Error(), "verify control image part") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoaderVerifiesDeploymentFilesBeforeDocker(t *testing.T) {
	directory := t.TempDir()
	archiveName := "agenthub-v1.0.0.tar.gz"
	archive := []byte("archive")
	writeTestFile(t, filepath.Join(directory, archiveName), archive)
	deploymentName := "agenthub-offline-compose.yaml"
	writeTestFile(t, filepath.Join(directory, deploymentName), []byte("tampered"))
	plan := testDownloadPlan(archiveName, "https://example.invalid/archive", archive)
	plan.DeploymentFiles = []DeploymentFile{{
		Name: deploymentName, Size: int64(len("expected")), SHA256: testDigest([]byte("expected")),
	}}
	err := (Loader{DockerBinary: filepath.Join(directory, "does-not-exist")}).Load(context.Background(), plan, directory)
	if err == nil || !strings.Contains(err.Error(), "verify deployment file") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestFetcherAndLoaderRejectSymlinkedTransferFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require elevated privileges on Windows")
	}
	body := []byte("archive")
	name := "agenthub-v1.0.0.tar.gz"
	plan := testDownloadPlan(name, "https://example.invalid/archive", body)
	directory := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside")
	writeTestFile(t, target, body)
	if err := os.Symlink(target, filepath.Join(directory, name)); err != nil {
		t.Fatal(err)
	}

	if err := (Fetcher{}).Fetch(context.Background(), plan, directory); err == nil || !strings.Contains(err.Error(), "symbolic links") {
		t.Fatalf("Fetch() symlink error = %v", err)
	}
	if err := (Loader{DockerBinary: filepath.Join(directory, "does-not-exist")}).Load(context.Background(), plan, directory); err == nil || !strings.Contains(err.Error(), "symbolic links") {
		t.Fatalf("Load() symlink error = %v", err)
	}
}

func TestFetcherAndLoaderRejectSymlinkedTransferDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require elevated privileges on Windows")
	}
	body := []byte("archive")
	plan := testDownloadPlan("agenthub-v1.0.0.tar.gz", "https://example.invalid/archive", body)
	realDirectory := t.TempDir()
	symlinkDirectory := filepath.Join(t.TempDir(), "transfer")
	if err := os.Symlink(realDirectory, symlinkDirectory); err != nil {
		t.Fatal(err)
	}

	if err := (Fetcher{}).Fetch(context.Background(), plan, symlinkDirectory); err == nil || !strings.Contains(err.Error(), "symbolic links") {
		t.Fatalf("Fetch() directory symlink error = %v", err)
	}
	if err := (Loader{}).Load(context.Background(), plan, symlinkDirectory); err == nil || !strings.Contains(err.Error(), "symbolic links") {
		t.Fatalf("Load() directory symlink error = %v", err)
	}
}

func TestLoaderRequiresExactImageReferenceAfterLoad(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	directory := t.TempDir()
	name := "agenthub-v1.0.0.tar.gz"
	body := []byte("archive")
	writeTestFile(t, filepath.Join(directory, name), body)
	script := filepath.Join(directory, "fake-docker")
	writeTestFile(t, script, []byte("#!/bin/sh\nif [ \"$1\" = load ]; then cat >/dev/null; exit 0; fi\nif [ \"$1\" = image ] && [ \"$2\" = inspect ]; then exit 1; fi\nexit 9\n"))
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}

	err := (Loader{DockerBinary: script}).Load(context.Background(), testDownloadPlan(name, "https://example.invalid/archive", body), directory)
	if err == nil || !strings.Contains(err.Error(), "verify loaded image agenthub:v1.0.0") {
		t.Fatalf("Load() image verification error = %v", err)
	}
}

func testDownloadPlan(name, url string, body []byte) *Plan {
	return &Plan{Images: []Image{{
		ID: "control", Image: "agenthub", Tag: "v1.0.0",
		Artifact: Artifact{LogicalName: name, Parts: []Part{{Name: name, URL: url, Size: int64(len(body)), SHA256: testDigest(body)}}},
	}}}
}
