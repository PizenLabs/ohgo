package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/update"
)

func postUpdateStore(t *testing.T) *update.TransitionStore {
	t.Helper()
	return update.NewTransitionStoreAt(filepath.Join(t.TempDir(), "update-transition.json"))
}

func atTransitionTime() time.Time {
	return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
}

func emptyChangelog() (update.Changelog, error) {
	return update.Changelog{Schema: 1, Releases: map[string]update.ReleaseNotes{}}, nil
}

// The startup card exists only because an explicit update created a pending
// transition for the release that is actually running now.
func TestPendingPostUpdateRequiresATransitionForTheRunningRelease(t *testing.T) {
	updateStore := postUpdateStore(t)
	if _, _, ok := pendingPostUpdate(updateStore, "v0.8.3", emptyChangelog); ok {
		t.Fatal("no transition authorized the What's New card")
	}

	if err := updateStore.Record(update.OperationUpdate, "v0.8.2", "v0.8.3", atTransitionTime()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	notice, acknowledge, ok := pendingPostUpdate(updateStore, "v0.8.3", emptyChangelog)
	if !ok {
		t.Fatal("a pending matching transition did not authorize the card")
	}
	if notice.Version != "v0.8.3" {
		t.Fatalf("notice version = %q", notice.Version)
	}
	if acknowledge == nil {
		t.Fatal("the authorized card has no acknowledgement path")
	}

	if _, _, ok := pendingPostUpdate(updateStore, "v0.8.4", emptyChangelog); ok {
		t.Fatal("a later running binary authorized the card")
	}
	if _, _, ok := pendingPostUpdate(updateStore, "v0.8.2", emptyChangelog); ok {
		t.Fatal("an earlier running binary authorized the card")
	}
}

func TestAcknowledgedTransitionDoesNotPresentTheCardAgain(t *testing.T) {
	updateStore := postUpdateStore(t)
	if err := updateStore.Record(update.OperationUpdate, "v0.8.2", "v0.8.3", atTransitionTime()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	_, acknowledge, ok := pendingPostUpdate(updateStore, "v0.8.3", emptyChangelog)
	if !ok {
		t.Fatal("the pending transition did not authorize the card")
	}
	// Merely deciding to present the card must not consume the notice.
	if _, pending := updateStore.Pending("v0.8.3"); !pending {
		t.Fatal("deciding to present the card consumed the transition")
	}
	if err := acknowledge(); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if _, _, ok := pendingPostUpdate(updateStore, "v0.8.3", emptyChangelog); ok {
		t.Fatal("the card was authorized again after acknowledgement")
	}
}

func TestRollbackNeverPresentsThePostUpdateCard(t *testing.T) {
	updateStore := postUpdateStore(t)
	if err := updateStore.Record(update.OperationRollback, "v0.8.3", "v0.8.2", atTransitionTime()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, _, ok := pendingPostUpdate(updateStore, "v0.8.2", emptyChangelog); ok {
		t.Fatal("a rollback authorized the What's New card")
	}
}

func TestOnlyTheInstalledReleasesNotesAreUsed(t *testing.T) {
	updateStore := postUpdateStore(t)
	if err := updateStore.Record(update.OperationUpdate, "v0.8.2", "v0.8.3", atTransitionTime()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	load := func() (update.Changelog, error) {
		return update.Changelog{Schema: 1, Releases: map[string]update.ReleaseNotes{
			"v0.8.2": {Title: "old notes", Changes: []update.Change{{ID: "old", Type: update.ChangeFixed, Title: "Old change"}}},
			"v0.8.3": {Title: "new notes", Changes: []update.Change{{ID: "new", Type: update.ChangeAdded, Title: "New change"}}},
			"v0.8.4": {Title: "future notes", Changes: []update.Change{{ID: "future", Type: update.ChangeAdded, Title: "Future change"}}},
		}}, nil
	}
	notice, _, ok := pendingPostUpdate(updateStore, "v0.8.3", load)
	if !ok {
		t.Fatal("the pending transition did not authorize the card")
	}
	if notice.Title != "new notes" {
		t.Fatalf("title = %q, want the installed release's notes", notice.Title)
	}
	if len(notice.Changes) != 1 || notice.Changes[0].Title != "New change" {
		t.Fatalf("changes = %+v, want only v0.8.3", notice.Changes)
	}
}

func TestMissingOrMalformedChangelogPreservesTheAcknowledgement(t *testing.T) {
	cases := map[string]func() (update.Changelog, error){
		"missing release": emptyChangelog,
		"load error": func() (update.Changelog, error) {
			return update.Changelog{}, errors.New("malformed changelog")
		},
		"nil loader": nil,
	}
	for name, load := range cases {
		t.Run(name, func(t *testing.T) {
			updateStore := postUpdateStore(t)
			if err := updateStore.Record(update.OperationUpdate, "v0.8.2", "v0.8.3", atTransitionTime()); err != nil {
				t.Fatalf("Record: %v", err)
			}
			notice, acknowledge, ok := pendingPostUpdate(updateStore, "v0.8.3", load)
			if !ok {
				t.Fatal("a missing changelog blocked the acknowledgement")
			}
			if notice.Version != "v0.8.3" {
				t.Fatalf("notice version = %q", notice.Version)
			}
			if len(notice.Changes) != 0 {
				t.Fatalf("notice invented changes: %+v", notice.Changes)
			}
			if err := acknowledge(); err != nil {
				t.Fatalf("acknowledge: %v", err)
			}
			if _, pending := updateStore.Pending("v0.8.3"); pending {
				t.Fatal("acknowledgement did not consume the notice")
			}
		})
	}
}

// A plain version query must not consume the acknowledgement. The bad
// sequence /update -> exit -> ohgo version -> ohgo must still present the card.
func TestVersionCommandDoesNotConsumeTheNotice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("machine state is resolved from USERPROFILE on Windows")
	}
	t.Setenv("HOME", t.TempDir())
	updateStore, err := update.NewTransitionStore()
	if err != nil {
		t.Fatalf("NewTransitionStore: %v", err)
	}
	if err := updateStore.Record(update.OperationUpdate, "v0.8.2", "v0.8.3", atTransitionTime()); err != nil {
		t.Fatalf("Record: %v", err)
	}

	cmd := versionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out.String(), "ohgo ") {
		t.Fatalf("version output = %q", out.String())
	}
	if _, pending := updateStore.Pending("v0.8.3"); !pending {
		t.Fatal("ohgo version consumed the pending notice")
	}
}

// A non-interactive command observes; it never presents or consumes the
// post-update acknowledgement.
func TestNonInteractiveCheckDoesNotConsumeTheNotice(t *testing.T) {
	updateStore := postUpdateStore(t)
	if err := updateStore.Record(update.OperationUpdate, "v0.8.2", "v0.8.3", atTransitionTime()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	svc := &fakeUpdateService{status: update.Status{
		CurrentVersion: "v0.8.2",
		LatestVersion:  "v0.8.3",
		Available:      true,
		Reason:         update.ReasonAvailable,
	}}
	var out bytes.Buffer
	if err := executeUpdateTo(context.Background(), &out, svc, true); err != nil {
		t.Fatalf("executeUpdateTo: %v", err)
	}
	if svc.checks != 1 || svc.updates != 0 {
		t.Fatalf("service calls = %d checks, %d updates", svc.checks, svc.updates)
	}
	if _, pending := updateStore.Pending("v0.8.3"); !pending {
		t.Fatal("a check-only command consumed the pending notice")
	}
}
