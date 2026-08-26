package offlinebundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineComposeRequiresExternalPostgresWithoutBundlingIt(t *testing.T) {
	root := repositoryRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "deploy", "offline", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, forbidden := range []string{"  postgres:", "image: postgres", "postgres:17", "agenthub-postgres"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Errorf("offline Compose contains forbidden bundled database marker %q", forbidden)
		}
	}
	if !strings.Contains(text, "${AGENTHUB_POSTGRES_DSN:?external PostgreSQL DSN is required}") {
		t.Fatal("offline Compose does not fail when external AGENTHUB_POSTGRES_DSN is missing")
	}
	if !strings.Contains(text, "agenthub:${AGENTHUB_VERSION:?") {
		t.Fatal("offline Compose does not require an explicit released AgentHub image tag")
	}
	for _, required := range []string{
		"  agenthub-worker:",
		"entrypoint: [/app/agenthub-worker]",
		"depends_on: [agenthub]",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("offline Compose is missing worker setting %q", required)
		}
	}
	if count := strings.Count(text, "${AGENTHUB_POSTGRES_DSN:?external PostgreSQL DSN is required}"); count != 2 {
		t.Errorf("external PostgreSQL DSN requirement appears %d times, want API and worker", count)
	}
}

func TestOfflineEnvironmentExampleDoesNotUseLocalDatabaseFallback(t *testing.T) {
	root := repositoryRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "deploy", "offline", ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "AGENTHUB_POSTGRES_DSN=postgres://") || !strings.Contains(text, "sslmode=require") {
		t.Fatal("offline environment example does not show an authenticated external TLS PostgreSQL DSN")
	}
	for _, forbidden := range []string{"@postgres:5432", "sslmode=disable", "agenthub-local-database-password"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("offline environment example contains local database fallback %q", forbidden)
		}
	}
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
