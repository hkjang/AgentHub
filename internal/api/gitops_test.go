package api

import (
	"os"
	"strings"
	"testing"
)

// The exported name becomes a download filename, and an agent may be named in
// any language with any punctuation.
func TestSafeFileNameStaysUsable(t *testing.T) {
	cases := map[string]string{
		"payment-agent": "payment-agent",
		"결제 점검 에이전트":    "결제-점검-에이전트",
		"a/b\\c:d*e?":   "a-b-c-d-e",
		"  ":            "agent",
		"":              "agent",
		"...":           "agent",
		"Ops_Agent v2":  "Ops_Agent-v2",
	}
	for name, want := range cases {
		if got := safeFileName(name); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", name, got, want)
		}
	}
}

// The plain filename parameter of Content-Disposition cannot carry non-ASCII,
// so a Korean name still needs a usable fallback.
func TestAsciiFileNameFallsBackWithoutLosingUsability(t *testing.T) {
	if got := asciiFileName("결제 점검"); got != "agent" {
		t.Errorf("asciiFileName(korean) = %q, want the fallback", got)
	}
	if got := asciiFileName("payment 점검 agent"); got != "payment----agent" {
		t.Errorf("asciiFileName(mixed) = %q", got)
	}
	for _, r := range asciiFileName("결제-agent") {
		if r > 127 {
			t.Fatalf("asciiFileName kept a non-ASCII rune: %q", r)
		}
	}
}

func TestSafeFileNameNeverEmptyOrPathLike(t *testing.T) {
	for _, name := range []string{"../../etc/passwd", "/", "..", "결제"} {
		got := safeFileName(name)
		if got == "" {
			t.Errorf("safeFileName(%q) is empty", name)
		}
		for _, r := range got {
			if r == '/' || r == '\\' || r == '.' {
				t.Errorf("safeFileName(%q) = %q, which is path-like", name, got)
			}
		}
	}
}

// The runtime image decides which container an agent actually runs, and it was
// the one binding that did not travel.
//
// Measured on a running deployment: an agent pinned to a registered image
// exported a document with no mention of it, so importing that file anywhere —
// including back into the same cluster — produced an agent running whatever the
// default for its runtime type happens to be. Every other reference in this
// document already travels by name; this one travelled not at all.
func TestTheDocumentCarriesTheRuntimeImage(t *testing.T) {
	body, err := os.ReadFile("gitops.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, "RuntimeImage    string   `json:\"runtimeImage,omitempty\"`") {
		t.Error("the document has no field for the image, so an export cannot mention it")
	}
	if !strings.Contains(source, "document.Spec.RuntimeImage = names.images[deref(agent.RuntimeImageID)]") {
		t.Error("an agent's image is not written into the document it exports")
	}
	if !strings.Contains(source, "RuntimeImageID:    imageID,") {
		t.Error("an imported document's image is not resolved, so it arrives unpinned")
	}
	// And a document that names an image this cluster lacks is refused in the
	// file's own words: the lookup key pairs runtime type and version, and
	// reporting that pairing names something the file never said.
	if !strings.Contains(source, `missing = append(missing, "런타임 이미지 "+version)`) {
		t.Error("a missing image is reported as this platform's internal key rather than what the file says")
	}
	// A version is unique inside a runtime type and nowhere else. Keying by name
	// would bind the wrong image the day two runtimes share one.
	if !strings.Contains(source, "lookup.imageIDs[strings.ToLower(imageKey(item.RuntimeType, item.Version))] = item.ID") {
		t.Error("images are not keyed the way this deployment keys them")
	}
	if key := imageKey("orca", ""); key != "" {
		t.Errorf("an agent with no image produces the reference %q, which resolves to nothing and reports it missing", key)
	}
	if key := imageKey("orca", "0.3.1"); key != "orca/0.3.1" {
		t.Errorf("an image is named %q", key)
	}
}
