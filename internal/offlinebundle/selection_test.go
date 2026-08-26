package offlinebundle

import (
	"strings"
	"testing"
)

func TestPlanSelectionAddsDependenciesAndDeduplicatesImages(t *testing.T) {
	manifest := validTestManifest()
	tests := []struct {
		name      string
		selection Selection
		wantIDs   string
	}{
		{name: "none", selection: Selection{NoRuntimes: true}, wantIDs: "control"},
		{name: "same image runtimes deduplicate", selection: Selection{RuntimeTypes: []string{"opencode", "hermes", "opencode"}}, wantIDs: "control,base"},
		{name: "dependency", selection: Selection{RuntimeTypes: []string{"jupyter"}}, wantIDs: "control,qwencode,jupyter"},
		{name: "all", selection: Selection{AllRuntimes: true}, wantIDs: "control,base,qwencode,jupyter"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := manifest.Plan(test.selection)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, len(plan.Images))
			for index, image := range plan.Images {
				ids[index] = image.ID
			}
			if got := strings.Join(ids, ","); got != test.wantIDs {
				t.Fatalf("selected images = %s, want %s", got, test.wantIDs)
			}
			if len(plan.Prerequisites) != 1 || plan.Prerequisites[0].ID != PostgresPrerequisiteID || plan.Prerequisites[0].Bundled {
				t.Fatalf("plan PostgreSQL prerequisite = %#v", plan.Prerequisites)
			}
		})
	}
}

func TestPlanRequiresExactlyOneSelectionMode(t *testing.T) {
	manifest := validTestManifest()
	for name, selection := range map[string]Selection{
		"none chosen":         {},
		"list and all":        {RuntimeTypes: []string{"opencode"}, AllRuntimes: true},
		"list and none":       {RuntimeTypes: []string{"opencode"}, NoRuntimes: true},
		"all and none":        {AllRuntimes: true, NoRuntimes: true},
		"every mode selected": {RuntimeTypes: []string{"opencode"}, AllRuntimes: true, NoRuntimes: true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := manifest.Plan(selection); err == nil || !strings.Contains(err.Error(), "choose exactly one runtime mode") {
				t.Fatalf("Plan() error = %v", err)
			}
		})
	}
}

func TestPlanRejectsUnknownAndCustomRuntime(t *testing.T) {
	manifest := validTestManifest()
	for runtimeType, want := range map[string]string{
		"unknown": "unknown runtime type",
		"custom":  "administrator-provided image",
	} {
		t.Run(runtimeType, func(t *testing.T) {
			_, err := manifest.Plan(Selection{RuntimeTypes: []string{runtimeType}})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Plan() error = %v, want %q", err, want)
			}
		})
	}
}
