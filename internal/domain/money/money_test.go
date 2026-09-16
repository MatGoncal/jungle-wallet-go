package money

import (
	"errors"
	"testing"

	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
)

func mustParse(t *testing.T, amount, currency string) Money {
	t.Helper()
	m, err := Parse(amount, currency)
	if err != nil {
		t.Fatalf("Parse(%q, %q): %v", amount, currency, err)
	}
	return m
}

func TestParseValid(t *testing.T) {
	tests := []struct {
		amount string
		want   int64
	}{
		{"0", 0},
		{"0.0", 0},
		{"0.00", 0},
		{"25", 2500},
		{"25.5", 2550},
		{"25.50", 2550},
	}
	for _, tt := range tests {
		t.Run(tt.amount, func(t *testing.T) {
			m, err := Parse(tt.amount, "brl")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m.AmountMinor() != tt.want {
				t.Fatalf("AmountMinor() = %d, want %d", m.AmountMinor(), tt.want)
			}
			if m.Currency() != "BRL" {
				t.Fatalf("Currency() = %q, want BRL", m.Currency())
			}
		})
	}
}

func TestParseInvalid(t *testing.T) {
	tests := []struct {
		name   string
		amount string
	}{
		{"empty", ""},
		{"spaces", "1 0"},
		{"leading space", " 10"},
		{"NaN", "NaN"},
		{"Infinity", "Infinity"},
		{"inf", "inf"},
		{"scientific lower", "1e2"},
		{"scientific upper", "1E10"},
		{"explicit plus", "+10"},
		{"explicit minus", "-10"},
		{"three decimals", "1.234"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.amount, "USD")
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, apperr.ErrInvalidArgument) && !errors.Is(err, apperr.ErrOverflow) {
				var f *apperr.Failure
				if errors.As(err, &f) && f.Code == apperr.CodeInvalidAmount {
					return
				}
				t.Fatalf("expected invalid amount failure, got %v", err)
			}
		})
	}
}

func TestStringTwoDecimals(t *testing.T) {
	tests := []struct {
		parse string
		want  string
		minor int64
	}{
		{"0", "0.00", 0},
		{"25", "25.00", 2500},
		{"25.5", "25.50", 2550},
	}
	for _, tt := range tests {
		t.Run(tt.parse, func(t *testing.T) {
			m := mustParse(t, tt.parse, "EUR")
			if got := m.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
	m, err := FromMinor(-105, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if m.String() != "-1.05" {
		t.Fatalf("negative String() = %q", m.String())
	}
}

func TestAddSubNegOverflow(t *testing.T) {
	maxM, err := FromMinor(maxInt64, "USD")
	if err != nil {
		t.Fatal(err)
	}
	one, _ := FromMinor(1, "USD")
	_, addErr := maxM.Add(one)
	if addErr == nil {
		t.Fatal("Add overflow expected")
	}
	if !errors.Is(addErr, apperr.ErrOverflow) {
		t.Fatalf("Add: want ErrOverflow, got %v", addErr)
	}

	minM, err := FromMinor(minInt64, "USD")
	if err != nil {
		t.Fatal(err)
	}
	_, negErr := minM.Neg()
	if negErr == nil {
		t.Fatal("Neg overflow expected")
	}
	if !errors.Is(negErr, apperr.ErrOverflow) {
		t.Fatalf("Neg: want ErrOverflow, got %v", negErr)
	}

	minM2, _ := FromMinor(minInt64, "USD")
	_, subErr := minM2.Sub(one)
	if subErr == nil {
		t.Fatal("Sub overflow expected")
	}
	if !errors.Is(subErr, apperr.ErrOverflow) {
		t.Fatalf("Sub: want ErrOverflow, got %v", subErr)
	}
}

func TestCurrencyMismatchAddCmp(t *testing.T) {
	usd := mustParse(t, "10", "USD")
	brl := mustParse(t, "10", "BRL")
	_, addErr := usd.Add(brl)
	if addErr == nil {
		t.Fatal("Add mismatch expected")
	}
	if !errors.Is(addErr, apperr.ErrCurrencyMismatch) {
		t.Fatalf("Add: %v", addErr)
	}
	_, cmpErr := usd.Cmp(brl)
	if cmpErr == nil {
		t.Fatal("Cmp mismatch expected")
	}
	if !errors.Is(cmpErr, apperr.ErrCurrencyMismatch) {
		t.Fatalf("Cmp: %v", cmpErr)
	}
}

func TestZero(t *testing.T) {
	z, err := Zero("usd")
	if err != nil {
		t.Fatal(err)
	}
	if !z.IsZero() || z.Currency() != "USD" {
		t.Fatalf("Zero: %+v", z)
	}
	if _, err := Zero("US"); err == nil {
		t.Fatal("invalid currency expected")
	}
}

func TestFromMinor(t *testing.T) {
	m, err := FromMinor(12345, "gbp")
	if err != nil {
		t.Fatal(err)
	}
	if m.AmountMinor() != 12345 || m.Currency() != "GBP" {
		t.Fatalf("FromMinor: %v", m)
	}
}

func TestParseHugeOverflow(t *testing.T) {
	// Integer part large enough to overflow when scaled by 100
	huge := "9223372036854775807"
	_, err := Parse(huge, "USD")
	if err == nil {
		t.Fatal("expected overflow")
	}
	if !errors.Is(err, apperr.ErrOverflow) {
		t.Fatalf("got %v", err)
	}
}
