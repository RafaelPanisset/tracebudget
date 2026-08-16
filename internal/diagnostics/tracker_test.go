package diagnostics

import (
	"testing"
	"time"
)

func TestIntegrityFailureCannotBeCleared(t *testing.T) {
	tracker := NewTracker()
	tracker.MarkIntegrityFailure("overload")
	tracker.AddReceived(4)
	tracker.AddSQLiteWriteDuration(25 * time.Millisecond)
	snapshot := tracker.Snapshot()
	if !snapshot.IntegrityFailed || snapshot.Received != 4 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if len(snapshot.IntegrityReasons) != 1 || snapshot.IntegrityReasons[0] != "overload" {
		t.Fatalf("unexpected reasons: %#v", snapshot.IntegrityReasons)
	}
	if snapshot.SQLiteWriteDuration != 25*time.Millisecond {
		t.Fatalf("unexpected write duration: %s", snapshot.SQLiteWriteDuration)
	}
}
