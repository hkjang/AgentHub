package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/buildinfo"
)

type fakeStarterTemplateStore struct {
	ownerID   string
	ownerErr  error
	seedErr   error
	seedOwner string
}

func (f *fakeStarterTemplateStore) StarterTemplateOwnerID(context.Context) (string, error) {
	return f.ownerID, f.ownerErr
}

func (f *fakeStarterTemplateStore) SeedTemplates(_ context.Context, owner string) error {
	f.seedOwner = owner
	return f.seedErr
}

func TestStarterTemplatesUseTheAttributedAdminWithoutAuthenticating(t *testing.T) {
	store := &fakeStarterTemplateStore{ownerID: "admin-1"}
	if err := seedStarterTemplates(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if store.seedOwner != "admin-1" {
		t.Fatalf("starter templates were attributed to %q, want the first active administrator", store.seedOwner)
	}
}

func TestStarterTemplateOwnerLookupFailuresAreReported(t *testing.T) {
	lookupErr := errors.New("database unavailable")
	for name, store := range map[string]*fakeStarterTemplateStore{
		"lookup failure": {ownerErr: lookupErr},
		"no admin":       {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := seedStarterTemplates(context.Background(), store); err == nil {
				t.Fatal("starter template seeding failure was hidden")
			}
			if store.seedOwner != "" {
				t.Fatalf("templates were seeded despite the owner lookup failure: %q", store.seedOwner)
			}
		})
	}
}

func TestStarterTemplateSeedFailuresAreReported(t *testing.T) {
	seedErr := errors.New("template insert rejected")
	store := &fakeStarterTemplateStore{ownerID: "admin-1", seedErr: seedErr}
	if err := seedStarterTemplates(context.Background(), store); !errors.Is(err, seedErr) {
		t.Fatalf("seedStarterTemplates() error = %v, want %v", err, seedErr)
	}
}

func TestVersionCommandWorksWithoutDeploymentConfiguration(t *testing.T) {
	for _, name := range []string{
		"AGENTHUB_POSTGRES_DSN",
		"AGENTHUB_BOOTSTRAP_ADMIN",
		"AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD",
		"AGENTHUB_ENCRYPTION_KEY",
	} {
		t.Setenv(name, "")
	}

	var output bytes.Buffer
	handled, err := runInfoCommand([]string{"version", "--json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("version command was not handled")
	}
	var info buildinfo.Info
	if err := json.Unmarshal(output.Bytes(), &info); err != nil {
		t.Fatalf("version output is not JSON: %v\n%s", err, output.String())
	}
	if info != buildinfo.Current() {
		t.Fatalf("version output = %#v, want %#v", info, buildinfo.Current())
	}
}

func TestVersionCommandHasPlainAndFailClosedForms(t *testing.T) {
	var output bytes.Buffer
	handled, err := runInfoCommand([]string{"version"}, &output)
	if err != nil || !handled {
		t.Fatalf("plain version command handled=%v error=%v", handled, err)
	}
	if got := strings.TrimSpace(output.String()); got != buildinfo.Version {
		t.Fatalf("plain version = %q, want %q", got, buildinfo.Version)
	}
	if handled, err := runInfoCommand([]string{"version", "--yaml"}, &output); !handled || err == nil {
		t.Fatalf("invalid version option handled=%v error=%v", handled, err)
	}
	if handled, err := runInfoCommand([]string{"serve"}, &output); handled || err != nil {
		t.Fatalf("unrelated command handled=%v error=%v", handled, err)
	}
}
