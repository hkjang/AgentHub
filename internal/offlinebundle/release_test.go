package offlinebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePrefersLocalCurrentAsset(t *testing.T) {
	directory := t.TempDir()
	body := []byte("current archive")
	name := "agenthub-v0.220.0.tar.gz"
	writeTestFile(t, filepath.Join(directory, name), body)
	resolver := ReleaseResolver{
		CurrentRelease: "v0.220.0",
		Repository:     "owner/repository",
		LocalAssets:    directory,
		Releases: []GitHubRelease{{TagName: "v0.220.0", Assets: []GitHubAsset{
			testAsset(name, "published archive"),
		}}},
	}
	artifact, err := resolver.Resolve(name)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.SourceRelease != "v0.220.0" || len(artifact.Parts) != 1 {
		t.Fatalf("artifact = %#v", artifact)
	}
	if got := artifact.Parts[0].SHA256; got != testDigest(body) {
		t.Fatalf("local SHA-256 = %s, want %s", got, testDigest(body))
	}
	if !strings.Contains(artifact.Parts[0].URL, "/owner/repository/releases/download/v0.220.0/") {
		t.Fatalf("local URL = %s", artifact.Parts[0].URL)
	}
}

func TestResolveUsesNewestStablePriorRelease(t *testing.T) {
	name := "agenthub-openhands-v1.43.1.tar.gz"
	resolver := ReleaseResolver{
		CurrentRelease: "v0.220.0",
		Releases: []GitHubRelease{
			{TagName: "v0.217.0", Assets: []GitHubAsset{testAsset(name, "old")}},
			{TagName: "v0.221.0", Assets: []GitHubAsset{testAsset(name, "future")}},
			{TagName: "v0.219.0", Assets: []GitHubAsset{testAsset(name, "newest")}},
			{TagName: "v0.219.1-rc.1", Assets: []GitHubAsset{testAsset(name, "prerelease tag")}},
			{TagName: "v0.219.1", Prerelease: true, Assets: []GitHubAsset{testAsset(name, "prerelease")}},
			{TagName: "v0.219.2", Draft: true, Assets: []GitHubAsset{testAsset(name, "draft")}},
		},
	}
	artifact, err := resolver.Resolve(name)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.SourceRelease != "v0.219.0" {
		t.Fatalf("source release = %s, want v0.219.0", artifact.SourceRelease)
	}
}

func TestResolveOrdersContiguousSplitParts(t *testing.T) {
	name := "agenthub-base-v0.13.0.tar.gz"
	resolver := ReleaseResolver{CurrentRelease: "v0.220.0", Releases: []GitHubRelease{{
		TagName: "v0.218.0",
		Assets: []GitHubAsset{
			testAsset(name+".part-ac", "three"),
			testAsset(name+".part-aa", "one"),
			testAsset(name+".part-ab", "two"),
		},
	}}}
	artifact, err := resolver.Resolve(name)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{artifact.Parts[0].Name, artifact.Parts[1].Name, artifact.Parts[2].Name}
	want := []string{name + ".part-aa", name + ".part-ab", name + ".part-ac"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("part order = %v, want %v", got, want)
	}
}

func TestResolveFailsClosedOnMalformedNewestCopy(t *testing.T) {
	name := "agenthub-orca-v0.4.0.tar.gz"
	tests := []struct {
		name   string
		assets []GitHubAsset
		want   string
	}{
		{
			name:   "gap",
			assets: []GitHubAsset{testAsset(name+".part-aa", "one"), testAsset(name+".part-ac", "three")},
			want:   "want contiguous",
		},
		{
			name: "missing digest",
			assets: []GitHubAsset{{
				Name: name, BrowserDownloadURL: testURL("v0.219.0", name), Size: 10,
			}},
			want: "missing a valid SHA-256",
		},
		{
			name:   "single and split",
			assets: []GitHubAsset{testAsset(name, "whole"), testAsset(name+".part-aa", "one")},
			want:   "both a single archive and split parts",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := ReleaseResolver{CurrentRelease: "v0.220.0", Releases: []GitHubRelease{
				{TagName: "v0.219.0", Assets: test.assets},
				{TagName: "v0.218.0", Assets: []GitHubAsset{testAsset(name, "valid older copy")}},
			}}
			_, err := resolver.Resolve(name)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestResolveNeverCombinesReleases(t *testing.T) {
	name := "agenthub-jupyter-v0.1.0.tar.gz"
	resolver := ReleaseResolver{CurrentRelease: "v0.220.0", Releases: []GitHubRelease{
		{TagName: "v0.219.0", Assets: []GitHubAsset{testAsset(name+".part-ab", "two")}},
		{TagName: "v0.218.0", Assets: []GitHubAsset{testAsset(name+".part-aa", "one")}},
	}}
	_, err := resolver.Resolve(name)
	if err == nil || !strings.Contains(err.Error(), "want contiguous") {
		t.Fatalf("Resolve() error = %v, want a contiguous-part failure", err)
	}
}

func TestValidatePartsRejectsMoreThanZZ(t *testing.T) {
	parts := make([]Part, 677)
	if err := validateParts("agenthub-v1.0.0.tar.gz", parts); err == nil || !strings.Contains(err.Error(), "maximum is 676") {
		t.Fatalf("validateParts() error = %v", err)
	}
}

func TestLoadReleaseIndexRejectsTrailingValue(t *testing.T) {
	if _, err := LoadReleaseIndex(strings.NewReader(`[] {}`)); err == nil {
		t.Fatal("LoadReleaseIndex accepted trailing JSON")
	}
}

func testAsset(name, body string) GitHubAsset {
	return GitHubAsset{
		Name:               name,
		BrowserDownloadURL: testURL("v0.219.0", name),
		Size:               int64(len(body)),
		Digest:             "sha256:" + testDigest([]byte(body)),
	}
}

func testURL(release, name string) string {
	return "https://github.example/owner/repository/releases/download/" + release + "/" + name
}

func testDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func writeTestFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}
