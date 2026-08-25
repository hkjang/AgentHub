package main

import (
	"context"
	"errors"
	"testing"
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
