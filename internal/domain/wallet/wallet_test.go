package wallet

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
)

func newUUID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}

func mustMoney(t *testing.T, s string) money.Money {
	t.Helper()
	m, err := money.Parse(s, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCreateVsRehydrate(t *testing.T) {
	id := newUUID(t)
	player := newUUID(t)
	zero := mustMoney(t, "0")
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	w, err := Create(id, player, zero, now)
	if err != nil {
		t.Fatal(err)
	}
	if w.Version() != 1 {
		t.Fatalf("Create version = %d, want 1", w.Version())
	}

	later := now.Add(time.Hour)
	w2, err := Rehydrate(id, player, mustMoney(t, "100.00"), 42, now, later)
	if err != nil {
		t.Fatal(err)
	}
	if w2.Version() != 42 {
		t.Fatalf("Rehydrate changed version unexpectedly: got %d", w2.Version())
	}
	if w2.Balance().String() != "100.00" {
		t.Fatalf("balance = %s", w2.Balance().String())
	}
}

func TestCreditDebitBalanceAndVersion(t *testing.T) {
	id := newUUID(t)
	player := newUUID(t)
	now := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	w, err := Create(id, player, mustMoney(t, "100"), now)
	if err != nil {
		t.Fatal(err)
	}

	amt := mustMoney(t, "25.50")
	res, err := w.Credit(newUUID(t), newUUID(t), amt, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	w = res.Wallet
	if w.Balance().String() != "125.50" {
		t.Fatalf("after credit: %s", w.Balance().String())
	}
	if w.Version() != 2 {
		t.Fatalf("version after credit = %d", w.Version())
	}
	if res.Entry.Direction() != DirectionCredit {
		t.Fatal("expected credit entry")
	}

	debitAmt := mustMoney(t, "25.50")
	res, err = w.Debit(newUUID(t), newUUID(t), debitAmt, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	w = res.Wallet
	if w.Balance().String() != "100.00" {
		t.Fatalf("after debit: %s", w.Balance().String())
	}
	if w.Version() != 3 {
		t.Fatalf("version after debit = %d", w.Version())
	}
}

func TestDebitInsufficientFunds(t *testing.T) {
	w, _ := Create(newUUID(t), newUUID(t), mustMoney(t, "10"), time.Now().UTC())
	beforeVer := w.Version()
	_, err := w.Debit(newUUID(t), newUUID(t), mustMoney(t, "10.01"), time.Now().UTC())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, apperr.ErrInsufficientFunds) {
		t.Fatalf("errors.Is insufficient: %v", err)
	}
	var f *apperr.Failure
	if !errors.As(err, &f) || f.Code != apperr.CodeInsufficientFunds {
		t.Fatalf("Failure code: %v", err)
	}
	if w.Version() != beforeVer {
		t.Fatalf("version changed on failed debit: %d -> should stay %d", w.Version(), beforeVer)
	}
	if w.Balance().String() != "10.00" {
		t.Fatalf("balance changed on failed debit")
	}
}

func TestCurrencyMismatchCreditDebit(t *testing.T) {
	w, _ := Create(newUUID(t), newUUID(t), mustMoney(t, "10"), time.Now().UTC())
	usd, _ := money.Parse("5", "USD")
	_, err := w.Credit(newUUID(t), newUUID(t), usd, time.Now().UTC())
	if err == nil {
		t.Fatal("credit mismatch expected")
	}
	if !errors.Is(err, apperr.ErrCurrencyMismatch) {
		t.Fatalf("credit: %v", err)
	}
	_, err = w.Debit(newUUID(t), newUUID(t), usd, time.Now().UTC())
	if err == nil {
		t.Fatal("debit mismatch expected")
	}
	if !errors.Is(err, apperr.ErrCurrencyMismatch) {
		t.Fatalf("debit: %v", err)
	}
}

func TestLedgerEntryBalanceAfter(t *testing.T) {
	wid := newUUID(t)
	tid := newUUID(t)
	eid := newUUID(t)
	before := mustMoney(t, "100")
	amount := mustMoney(t, "10")
	wrongAfter := mustMoney(t, "95") // should be 90
	_, err := NewLedgerEntry(eid, wid, tid, DirectionDebit, amount, before, wrongAfter, time.Now().UTC())
	if err == nil {
		t.Fatal("expected invariant error")
	}
	if !errors.Is(err, apperr.ErrInvariantViolation) {
		t.Fatalf("got %v", err)
	}

	after := mustMoney(t, "90")
	entry, err := NewLedgerEntry(eid, wid, tid, DirectionDebit, amount, before, after, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if entry.BalanceAfter().String() != "90.00" {
		t.Fatalf("BalanceAfter = %s", entry.BalanceAfter().String())
	}
}

func TestVersionOnlyOnSuccessfulBalanceChange(t *testing.T) {
	w, _ := Create(newUUID(t), newUUID(t), mustMoney(t, "5"), time.Now().UTC())
	v := w.Version()

	_, err := w.Debit(newUUID(t), newUUID(t), mustMoney(t, "0"), time.Now().UTC())
	if err == nil {
		t.Fatal("zero debit should fail")
	}
	if w.Version() != v {
		t.Fatalf("version after invalid debit: %d", w.Version())
	}

	_, err = w.Credit(newUUID(t), newUUID(t), mustMoney(t, "0"), time.Now().UTC())
	if err == nil {
		t.Fatal("zero credit should fail")
	}
	if w.Version() != v {
		t.Fatalf("version after invalid credit: %d", w.Version())
	}
}
