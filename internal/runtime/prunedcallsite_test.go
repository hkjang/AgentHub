package runtime

import (
	"os"
	"strings"
	"testing"
)

// The report is only worth writing if it runs on both writes: a spawn, and the
// sync that pushes a changed setting to every existing runtime. The second is the
// one that matters after an upgrade, since the objects already exist.
func TestBothWritesReportWhatWasPruned(t *testing.T) {
	body, err := os.ReadFile("kubernetes.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, fn := range []string{"func (k *KubernetesSpawner) Spawn(", "func (k *KubernetesSpawner) Sync("} {
		at := strings.Index(source, fn)
		if at < 0 {
			t.Fatalf("%s is gone; this guard is reading nothing", fn)
		}
		block := source[at:]
		if end := strings.Index(block, "\n}\n"); end >= 0 {
			block = block[:end]
		}
		if !strings.Contains(block, "reportPruned(") {
			t.Errorf("%s writes the object without checking what came back; a field the CRD drops stays invisible", strings.TrimPrefix(fn, "func (k *KubernetesSpawner) "))
		}
	}
}
