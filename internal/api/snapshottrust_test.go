package api

import (
	"os"
	"strings"
	"testing"
)

// A snapshot row is this platform's memory of something somebody else stores.
//
// The row said "ready" and was never asked again, so a snapshot deleted in the
// cluster stayed restorable on this screen — and the way to find out was to
// restore from it, which is the moment its owner has already deleted the thing
// they meant to get back.
func TestASnapshotThatClaimsToBeRestorableIsAsked(t *testing.T) {
	body, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)

	at := strings.Index(source, "func (s *Server) workspaceSnapshots(")
	if at < 0 {
		t.Fatal("the snapshot listing is gone; this guard is reading nothing")
	}
	listing := source[at:]
	if end := strings.Index(listing, "\nfunc "); end >= 0 {
		listing = listing[:end]
	}
	// Ready ones are re-asked, not only the ones still being made.
	if !strings.Contains(listing, `item.Status != "ready"`) {
		t.Error("the listing only re-asks about snapshots that are still being made, so one deleted in the cluster stays restorable here for ever")
	}
	if !strings.Contains(listing, "ErrSnapshotMissing") {
		t.Error("the listing cannot tell a deleted snapshot from a cluster that has no snapshot support")
	}

	// And restoring asks before it promises anything.
	at = strings.Index(source, "func (s *Server) restoreWorkspaceSnapshot(")
	restore := source[at:]
	if end := strings.Index(restore, "\nfunc "); end >= 0 {
		restore = restore[:end]
	}
	if !strings.Contains(restore, "ErrSnapshotMissing") {
		t.Error("restore does not check that the snapshot still exists, so it fails halfway with whatever the cluster says")
	}
	// Before the workspace is created, not after: the check is worthless if the
	// row already exists by the time it runs.
	if strings.Index(restore, "ErrSnapshotMissing") > strings.Index(restore, "RestoreWorkspaceSnapshot(") {
		t.Error("the check runs after the workspace has already been created")
	}
}
