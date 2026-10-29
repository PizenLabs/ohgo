package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixedTransitionTime() time.Time {
	return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
}

func TestTransitionRoundTripKeepsLifecycleTruth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-transition.json")
	store := NewTransitionStoreAt(path)
	want := NewVersionTransition(OperationUpdate, "v0.8.2", "v0.8.3", fixedTransitionTime())

	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok := store.Load()
	if !ok {
		t.Fatal("Load missed a record Save just wrote")
	}
	if got.Operation != OperationUpdate || got.From != "v0.8.2" || got.To != "v0.8.3" {
		t.Fatalf("Load = %+v", got)
	}
	if got.NoticeState != NoticePending || got.Status != TransitionCompleted {
		t.Fatalf("Load = %+v, want a completed pending update", got)
	}
	if !got.CompletedAt.Equal(fixedTransitionTime()) {
		t.Fatalf("CompletedAt = %v", got.CompletedAt)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("transition mode = %o, want 600", mode)
	}
}

func TestOnlyUpdateCreatesAPendingNotice(t *testing.T) {
	at := fixedTransitionTime()
	update := NewVersionTransition(OperationUpdate, "v0.8.2", "v0.8.3", at)
	if update.NoticeState != NoticePending {
		t.Fatalf("update notice = %q, want pending", update.NoticeState)
	}
	rollback := NewVersionTransition(OperationRollback, "v0.8.3", "v0.8.2", at)
	if rollback.NoticeState != NoticeNone {
		t.Fatalf("rollback notice = %q, want none", rollback.NoticeState)
	}
	if rollback.PendingFor("v0.8.2") {
		t.Fatal("a rollback authorized a post-update notice")
	}
}

// A pending notice belongs to the release the update installed. Version
// inequality alone must never authorize the card.
func TestPendingTransitionRequiresTheRunningRelease(t *testing.T) {
	store := NewTransitionStoreAt(filepath.Join(t.TempDir(), "update-transition.json"))
	if err := store.Record(OperationUpdate, "v0.8.2", "v0.8.3", fixedTransitionTime()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, ok := store.Pending("v0.8.4"); ok {
		t.Fatal("a different running release authorized the card")
	}
	if _, ok := store.Pending("v0.8.1"); ok {
		t.Fatal("an older running release authorized the card")
	}
	if _, ok := store.Pending("v0.8.3"); !ok {
		t.Fatal("the installed release did not authorize the card")
	}
	// Version spelling is presentation, not identity.
	if _, ok := store.Pending("0.8.3"); !ok {
		t.Fatal("a normalized release spelling did not authorize the card")
	}
}

func TestAcknowledgingConsumesTheNoticeExactlyOnce(t *testing.T) {
	store := NewTransitionStoreAt(filepath.Join(t.TempDir(), "update-transition.json"))
	if err := store.Record(OperationUpdate, "v0.8.2", "v0.8.3", fixedTransitionTime()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	changed, err := store.Acknowledge("v0.8.3")
	if err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if !changed {
		t.Fatal("Acknowledge did not consume a pending notice")
	}
	if _, ok := store.Pending("v0.8.3"); ok {
		t.Fatal("the notice remained pending after acknowledgement")
	}
	record, ok := store.Load()
	if !ok || record.NoticeState != NoticeAcknowledged {
		t.Fatalf("record = %+v, ok=%v", record, ok)
	}
	changed, err = store.Acknowledge("v0.8.3")
	if err != nil {
		t.Fatalf("second Acknowledge: %v", err)
	}
	if changed {
		t.Fatal("a second acknowledgement changed the record")
	}
}

func TestAcknowledgeRefusesAMismatchedOrRollbackRecord(t *testing.T) {
	store := NewTransitionStoreAt(filepath.Join(t.TempDir(), "update-transition.json"))
	if err := store.Record(OperationUpdate, "v0.8.2", "v0.8.3", fixedTransitionTime()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if changed, err := store.Acknowledge("v0.8.2"); err != nil || changed {
		t.Fatalf("mismatched Acknowledge = %v, %v; want false, nil", changed, err)
	}
	if _, ok := store.Pending("v0.8.3"); !ok {
		t.Fatal("a refused acknowledgement consumed the notice")
	}

	rollback := NewTransitionStoreAt(filepath.Join(t.TempDir(), "update-transition.json"))
	if err := rollback.Record(OperationRollback, "v0.8.3", "v0.8.2", fixedTransitionTime()); err != nil {
		t.Fatalf("Record rollback: %v", err)
	}
	if changed, err := rollback.Acknowledge("v0.8.2"); err != nil || changed {
		t.Fatalf("rollback Acknowledge = %v, %v; want false, nil", changed, err)
	}
}

func TestReUpdateAfterRollbackCreatesANewNotice(t *testing.T) {
	store := NewTransitionStoreAt(filepath.Join(t.TempDir(), "update-transition.json"))
	if err := store.Record(OperationUpdate, "v0.8.2", "v0.8.3", fixedTransitionTime()); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if changed, err := store.Acknowledge("v0.8.3"); err != nil || !changed {
		t.Fatalf("Acknowledge = %v, %v", changed, err)
	}
	if err := store.Record(OperationRollback, "v0.8.3", "v0.8.2", fixedTransitionTime()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, ok := store.Pending("v0.8.2"); ok {
		t.Fatal("rollback authorized a post-update notice")
	}
	if err := store.Record(OperationUpdate, "v0.8.2", "v0.8.3", fixedTransitionTime()); err != nil {
		t.Fatalf("second update: %v", err)
	}
	record, ok := store.Pending("v0.8.3")
	if !ok {
		t.Fatal("the second update did not create a new pending notice")
	}
	if record.From != "v0.8.2" || record.To != "v0.8.3" {
		t.Fatalf("second transition = %+v", record)
	}
}

// The documented transition shape is small enough that a user may write it by
// hand. An omitted schema is the current schema, not a foreign record.
func TestUnversionedTransitionUsesTheCurrentSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-transition.json")
	body := `{
		"operation": "update",
		"from": "v0.8.2",
		"to": "v0.8.3",
		"status": "completed",
		"post_update_notice": "pending"
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store := NewTransitionStoreAt(path)
	transition, ok := store.Pending("v0.8.3")
	if !ok {
		t.Fatal("an unversioned documented record was rejected")
	}
	if transition.SchemaVersion != TransitionSchemaVersion {
		t.Fatalf("schema = %d, want normalized to %d", transition.SchemaVersion, TransitionSchemaVersion)
	}
}

func TestCorruptTransitionIsAbsentNotPartiallyApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-transition.json")
	for _, body := range []string{
		"{",
		`{"schema_version":99,"operation":"update","status":"completed","to":"v0.8.3","post_update_notice":"pending"}`,
		`{"schema_version":1,"operation":"guess","status":"completed","to":"v0.8.3","post_update_notice":"pending"}`,
		`{"schema_version":1,"operation":"update","status":"completed","to":"","post_update_notice":"pending"}`,
		`{"schema_version":1,"operation":"update","status":"completed","to":"v0.8.3","post_update_notice":"later"}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		store := NewTransitionStoreAt(path)
		if _, ok := store.Load(); ok {
			t.Fatalf("Load accepted corrupt transition %q", body)
		}
		if _, ok := store.Pending("v0.8.3"); ok {
			t.Fatalf("a corrupt transition authorized a notice: %q", body)
		}
	}
}

// Observing a newer release is information, not authority: no transition and
// therefore no post-update notice may be created by a check.
func TestServiceCheckDoesNotCreateATransition(t *testing.T) {
	fixture := newUpdateFixture(t, "v1.0.0", "v2.0.0", "#!/bin/sh\necho ohgo v2.0.0\n", nil)
	status, err := fixture.service.Check(t.Context())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !status.Available {
		t.Fatalf("Check status = %+v, want the release available", status)
	}
	if _, ok := fixture.service.transition.Load(); ok {
		t.Fatal("a release observation created a version transition")
	}
}

func TestServiceUpdatePersistsAPendingTransition(t *testing.T) {
	fixture := newUpdateFixture(t, "v1.0.0", "v2.0.0", "#!/bin/sh\necho ohgo v2.0.0\n", nil)
	if _, err := fixture.service.Update(t.Context()); err != nil {
		t.Fatalf("Update: %v", err)
	}
	record, ok := fixture.service.transition.Pending("v2.0.0")
	if !ok {
		t.Fatal("a verified install did not create a pending post-update transition")
	}
	if record.Operation != OperationUpdate || record.From != "v1.0.0" || record.To != "v2.0.0" {
		t.Fatalf("transition = %+v", record)
	}
}

func TestNoOpAndFailedUpdatesDoNotCreateATransition(t *testing.T) {
	already := newUpdateFixture(t, "v2.0.0", "v2.0.0", "#!/bin/sh\necho ohgo v2.0.0\n", nil)
	if result, err := already.service.Update(t.Context()); err != nil || !result.AlreadyCurrent {
		t.Fatalf("Update = %+v, %v; want already current", result, err)
	}
	if _, ok := already.service.transition.Load(); ok {
		t.Fatal("an already-current observation created a transition")
	}

	failed := newUpdateFixture(t, "v1.0.0", "v2.0.0", "#!/bin/sh\necho ohgo v2.0.0\n",
		func(name string, _ []byte) []byte {
			return []byte("0000000000000000000000000000000000000000000000000000000000000000  " + name + "\n")
		})
	if _, err := failed.service.Update(t.Context()); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Update error = %v, want ErrChecksumMismatch", err)
	}
	if _, ok := failed.service.transition.Load(); ok {
		t.Fatal("a failed update created a transition")
	}
}
