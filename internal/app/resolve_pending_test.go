package app_test

import (
	"context"
	"testing"
)

func TestMemInbox_InsertReturnsExistingHash(t *testing.T) {
	uow := newMemUoW()
	inbox := &memInbox{u: uow}
	ok, _, err := inbox.Insert(context.Background(), "c", "m1", "hash-a")
	if err != nil || !ok {
		t.Fatalf("first insert ok=%v err=%v", ok, err)
	}
	ok, existing, err := inbox.Insert(context.Background(), "c", "m1", "hash-b")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected conflict")
	}
	if existing != "hash-a" {
		t.Fatalf("existing=%q want hash-a", existing)
	}
}
