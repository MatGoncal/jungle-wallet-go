package app_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
)

func TestLedgerCursorRoundTrip(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	ts := time.Date(2026, 9, 15, 12, 0, 0, 123456789, time.UTC)
	cur := app.EncodeLedgerCursor(ts, id)
	gotT, gotID, err := app.DecodeLedgerCursor(cur)
	if err != nil {
		t.Fatal(err)
	}
	if !gotT.Equal(ts) || gotID != id {
		t.Fatalf("roundtrip mismatch: %v %v", gotT, gotID)
	}
}

func TestLedgerCursorInvalid(t *testing.T) {
	if _, _, err := app.DecodeLedgerCursor("%%%"); err == nil {
		t.Fatal("expected error")
	}
}
