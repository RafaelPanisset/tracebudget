package diagnostics

import (
	"sync"
	"time"
)

type Snapshot struct {
	Received              int64
	Duplicates            int64
	Invalid               int64
	Incomplete            int64
	Unmatched             int64
	CurrentBuffer         int
	PeakBuffer            int
	SQLiteWriteDuration   time.Duration
	AssemblyDuration      time.Duration
	NormalizationDuration time.Duration
	ComparisonDuration    time.Duration
	IntegrityFailed       bool
	IntegrityReasons      []string
}

type Tracker struct {
	mu       sync.Mutex
	snapshot Snapshot
}

func NewTracker() *Tracker { return &Tracker{} }

func (t *Tracker) AddReceived(value int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snapshot.Received += int64(value)
}

func (t *Tracker) SetBufferDepth(value int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snapshot.CurrentBuffer = value
	if value > t.snapshot.PeakBuffer {
		t.snapshot.PeakBuffer = value
	}
}

func (t *Tracker) AddDuplicates(value int) { t.add(&t.snapshot.Duplicates, value) }
func (t *Tracker) AddInvalid(value int)    { t.add(&t.snapshot.Invalid, value) }
func (t *Tracker) AddIncomplete(value int) { t.add(&t.snapshot.Incomplete, value) }
func (t *Tracker) AddUnmatched(value int)  { t.add(&t.snapshot.Unmatched, value) }

func (t *Tracker) AddSQLiteWriteDuration(value time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snapshot.SQLiteWriteDuration += value
}

func (t *Tracker) add(target *int64, value int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	*target += int64(value)
}

func (t *Tracker) MarkIntegrityFailure(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snapshot.IntegrityFailed = true
	t.snapshot.IntegrityReasons = append(t.snapshot.IntegrityReasons, reason)
}

func (t *Tracker) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	copy := t.snapshot
	copy.IntegrityReasons = append([]string(nil), t.snapshot.IntegrityReasons...)
	return copy
}
