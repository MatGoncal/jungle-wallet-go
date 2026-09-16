package apperr

import (
	"errors"
	"testing"
)

func TestErrorsIsFailure(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{
			name:   "insufficient funds code",
			err:    NewFailure(CodeInsufficientFunds, "no money"),
			target: ErrInsufficientFunds,
			want:   true,
		},
		{
			name:   "currency mismatch",
			err:    NewFailure(CodeCurrencyMismatch, "x != y"),
			target: ErrCurrencyMismatch,
			want:   true,
		},
		{
			name:   "wrapped invalid argument",
			err:    WrapFailure(CodeInvalidInput, "bad", ErrInvalidArgument),
			target: ErrInvalidArgument,
			want:   true,
		},
		{
			name:   "wrong sentinel",
			err:    NewFailure(CodeInsufficientFunds, "no money"),
			target: ErrNotFound,
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errors.Is(tt.err, tt.target); got != tt.want {
				t.Fatalf("errors.Is() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestErrorsAsFailure(t *testing.T) {
	inner := NewFailure(CodeInvalidAmount, "bad amount")
	wrapped := WrapFailure(CodeInvalidInput, "wrap", inner)

	var f *Failure
	if !errors.As(wrapped, &f) {
		t.Fatal("errors.As failed")
	}
	if f.Code != CodeInvalidInput {
		t.Fatalf("Code = %q, want INVALID_INPUT", f.Code)
	}
	if f.Cause != inner {
		t.Fatal("expected cause to be inner failure")
	}

	direct := NewFailure(CodeKindNotAllowed, "nope")
	if !errors.As(direct, &f) {
		t.Fatal("errors.As on direct failure failed")
	}
	if f.Code != CodeKindNotAllowed {
		t.Fatalf("Code = %q", f.Code)
	}

	_, ok := AsFailure(direct)
	if !ok {
		t.Fatal("AsFailure failed")
	}
}
