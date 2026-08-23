package runtime

import (
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Two different 404s, two different things to do about them.
//
// A cluster with no VolumeSnapshot CRD and a snapshot somebody deleted both come
// back as "not found". They were reported as the same thing — the cluster
// lacking snapshot support — which sends an operator to install something that
// is already installed, while the real news is that what they were about to
// restore from is gone.
func TestAMissingSnapshotIsNotAMissingFeature(t *testing.T) {
	resource := schema.GroupResource{Group: "snapshot.storage.k8s.io", Resource: "volumesnapshots"}

	// One object, named: this snapshot is gone.
	gone := snapshotSupportError(apierrors.NewNotFound(resource, "workspace-snap-1"))
	if !errors.Is(gone, ErrSnapshotMissing) {
		t.Errorf("a deleted snapshot is reported as %v", gone)
	}
	if errors.Is(gone, ErrSnapshotsUnsupported) {
		t.Error("a deleted snapshot is reported as the cluster having no snapshot support, which sends somebody to install a CRD that is already there")
	}

	// The resource type itself, with nothing to name: this cluster has no
	// snapshots at all.
	absent := snapshotSupportError(&apierrors.StatusError{ErrStatus: metav1.Status{
		Status: metav1.StatusFailure, Code: 404, Reason: metav1.StatusReasonNotFound,
		Message: "the server could not find the requested resource",
	}})
	if !errors.Is(absent, ErrSnapshotsUnsupported) {
		t.Errorf("a cluster with no snapshot CRD is reported as %v", absent)
	}
	if errors.Is(absent, ErrSnapshotMissing) {
		t.Error("a cluster with no snapshot support is reported as one missing snapshot")
	}

	// And the mapper's own way of saying the type is unknown.
	noMatch := snapshotSupportError(&meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: resource.Group, Kind: "VolumeSnapshot"}})
	if !errors.Is(noMatch, ErrSnapshotsUnsupported) {
		t.Errorf("an unknown resource type is reported as %v", noMatch)
	}

	// Anything else is passed through: inventing either diagnosis for a timeout
	// or a refusal would be worse than saying what happened.
	other := errors.New("connection refused")
	if wrapped := snapshotSupportError(other); !errors.Is(wrapped, other) {
		t.Errorf("an unrelated failure was rewritten as %v", wrapped)
	}
}
