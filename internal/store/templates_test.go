package store

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

func TestEveryBuiltInRuntimeHasAStarterTemplate(t *testing.T) {
	covered := map[string]int{}
	slugs := map[string]string{}
	for _, template := range starterTemplates {
		if !runtimetype.IsSupported(template.runtime) {
			t.Errorf("starter template %q names unsupported runtime %q", template.slug, template.runtime)
		}
		for field, value := range map[string]string{
			"name": template.name, "slug": template.slug, "description": template.description,
			"category": template.category, "profile": template.profile, "prompt": template.prompt,
		} {
			if strings.TrimSpace(value) == "" {
				t.Errorf("starter template %q has an empty %s", template.slug, field)
			}
		}
		if previous, duplicate := slugs[template.slug]; duplicate {
			t.Errorf("starter template slug %q is shared by %s and %s", template.slug, previous, template.runtime)
		}
		slugs[template.slug] = template.runtime
		covered[template.runtime]++
	}

	for _, runtimeType := range runtimetype.Supported {
		// Custom needs an administrator-supplied image, command and port. A generic
		// card cannot create a valid instance, so it is intentionally not seeded.
		if runtimeType == runtimetype.Custom {
			continue
		}
		if covered[runtimeType] == 0 {
			t.Errorf("built-in runtime %q has no starter template and will be missing from the catalog", runtimeType)
		}
	}
}
