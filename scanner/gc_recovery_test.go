package scanner

import (
	"context"
	"testing"

	"github.com/navidrome/navidrome/model"
)

type gcRecoveryStore struct {
	model.DataStore
	calls int
}

func (s *gcRecoveryStore) WithTx(f func(model.DataStore) error, _ ...string) error { return f(s) }
func (s *gcRecoveryStore) GC(context.Context, ...int) error                        { s.calls++; return nil }

func TestFullScanRecoversPendingGC(t *testing.T) {
	for _, full := range []bool{false, true} {
		ds := &gcRecoveryStore{}
		s := &scannerImpl{ds: ds}
		state := &scanState{fullScan: full, progress: make(chan *ProgressInfo, 1)}
		if err := s.runGC(context.Background(), state)(); err != nil {
			t.Fatal(err)
		}
		if (ds.calls == 1) != full {
			t.Fatalf("full=%t, GC calls=%d", full, ds.calls)
		}
	}
}
