package db

import (
	"errors"
	"testing"
)

func TestIsLegacySTRMMigrationGapOnlyAcceptsExactKnownGap(t *testing.T) {
	if !isLegacySTRMMigrationGap(errors.New(legacySTRMMissingMigrationError)) {
		t.Fatal("known Redia STRM migration gap was not recognized")
	}
	for _, message := range []string{
		"error: found 2 missing migrations before current version 20251019000000:\n\tversion 20250823142158: migrations/20250823142158_make_playqueue_position_int.sql",
		"error: found 1 missing migrations before current version 20251019000001:\n\tversion 20250823142158: migrations/20250823142158_make_playqueue_position_int.sql",
		"error: found 1 missing migrations before current version 20251019000000:\n\tversion 20250823142159: migrations/unknown.sql",
	} {
		if isLegacySTRMMigrationGap(errors.New(message)) {
			t.Fatalf("unsafe migration gap was accepted: %s", message)
		}
	}
}
