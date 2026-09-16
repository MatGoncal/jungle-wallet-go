package money

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
)

const Scale int64 = 100

// Money is an immutable value object in minor units with an ISO 4217 currency.
// Limit with int64: about ±92.2 quadrillion minor units (~±922 quadrillion major units at scale 2).
type Money struct {
	amountMinor int64
	currency    string
}

func Zero(currency string) (Money, error) {
	cur, err := normalizeCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	return Money{amountMinor: 0, currency: cur}, nil
}

func FromMinor(amountMinor int64, currency string) (Money, error) {
	cur, err := normalizeCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	return Money{amountMinor: amountMinor, currency: cur}, nil
}

// Parse accepts "0", "0.0", "0.00", "25", "25.5", "25.50".
// Rejects empty, NaN, Infinity, scientific notation, explicit sign, spaces, and 3+ decimal places.
func Parse(amount string, currency string) (Money, error) {
	cur, err := normalizeCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	minor, err := parseDecimalToMinor(amount)
	if err != nil {
		return Money{}, err
	}
	return Money{amountMinor: minor, currency: cur}, nil
}

func (m Money) AmountMinor() int64 { return m.amountMinor }
func (m Money) Currency() string   { return m.currency }
func (m Money) IsZero() bool       { return m.amountMinor == 0 }
func (m Money) IsPositive() bool   { return m.amountMinor > 0 }
func (m Money) IsNegative() bool   { return m.amountMinor < 0 }

func (m Money) String() string {
	neg := m.amountMinor < 0
	v := m.amountMinor
	if neg {
		v = -v
	}
	major := v / Scale
	frac := v % Scale
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%02d", sign, major, frac)
}

func (m Money) Add(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	sum, err := addChecked(m.amountMinor, other.amountMinor)
	if err != nil {
		return Money{}, err
	}
	return Money{amountMinor: sum, currency: m.currency}, nil
}

func (m Money) Sub(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	diff, err := subChecked(m.amountMinor, other.amountMinor)
	if err != nil {
		return Money{}, err
	}
	return Money{amountMinor: diff, currency: m.currency}, nil
}

func (m Money) Neg() (Money, error) {
	if m.amountMinor == minInt64 {
		return Money{}, apperr.WrapFailure(apperr.CodeInvalidAmount, "negation overflow", apperr.ErrOverflow)
	}
	return Money{amountMinor: -m.amountMinor, currency: m.currency}, nil
}

func (m Money) Cmp(other Money) (int, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return 0, err
	}
	switch {
	case m.amountMinor < other.amountMinor:
		return -1, nil
	case m.amountMinor > other.amountMinor:
		return 1, nil
	default:
		return 0, nil
	}
}

func (m Money) Equal(other Money) bool {
	return m.amountMinor == other.amountMinor && m.currency == other.currency
}

func (m Money) requireSameCurrency(other Money) error {
	if m.currency == "" || other.currency == "" {
		return apperr.WrapFailure(apperr.CodeInvalidInput, "money currency not set", apperr.ErrInvalidArgument)
	}
	if m.currency != other.currency {
		return apperr.NewFailure(apperr.CodeCurrencyMismatch, fmt.Sprintf("currency %s != %s", m.currency, other.currency))
	}
	return nil
}

func normalizeCurrency(currency string) (string, error) {
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if len(cur) != 3 {
		return "", apperr.WrapFailure(apperr.CodeInvalidInput, "currency must be ISO 4217 (3 letters)", apperr.ErrInvalidArgument)
	}
	for _, r := range cur {
		if r < 'A' || r > 'Z' {
			return "", apperr.WrapFailure(apperr.CodeInvalidInput, "currency must be letters", apperr.ErrInvalidArgument)
		}
	}
	return cur, nil
}

func parseDecimalToMinor(amount string) (int64, error) {
	if amount == "" {
		return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "empty amount", apperr.ErrInvalidArgument)
	}
	for _, r := range amount {
		if unicode.IsSpace(r) {
			return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "amount must not contain spaces", apperr.ErrInvalidArgument)
		}
	}
	lower := strings.ToLower(amount)
	if lower == "nan" || lower == "inf" || lower == "+inf" || lower == "-inf" ||
		lower == "infinity" || lower == "+infinity" || lower == "-infinity" {
		return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "amount is not a finite decimal", apperr.ErrInvalidArgument)
	}
	if strings.ContainsAny(amount, "eE") {
		return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "scientific notation is not allowed", apperr.ErrInvalidArgument)
	}
	if amount[0] == '+' || amount[0] == '-' {
		return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "explicit sign is not allowed on external amounts", apperr.ErrInvalidArgument)
	}

	parts := strings.Split(amount, ".")
	if len(parts) > 2 {
		return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "invalid decimal format", apperr.ErrInvalidArgument)
	}
	majorPart := parts[0]
	fracPart := ""
	if len(parts) == 2 {
		fracPart = parts[1]
	}
	if majorPart == "" {
		return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "missing integer part", apperr.ErrInvalidArgument)
	}
	if len(fracPart) > 2 {
		return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "more than 2 decimal places", apperr.ErrInvalidArgument)
	}
	for _, r := range majorPart + fracPart {
		if r < '0' || r > '9' {
			return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "amount must be digits and optional dot", apperr.ErrInvalidArgument)
		}
	}
	for len(fracPart) < 2 {
		fracPart += "0"
	}

	major, err := parseUintStrict(majorPart)
	if err != nil {
		return 0, err
	}
	frac, err := parseUintStrict(fracPart)
	if err != nil {
		return 0, err
	}
	// major * 100 + frac with overflow check
	scaled, err := mulChecked(major, Scale)
	if err != nil {
		return 0, err
	}
	return addChecked(scaled, frac)
}

func parseUintStrict(s string) (int64, error) {
	var n int64
	for _, r := range s {
		digit := int64(r - '0')
		n2, err := mulChecked(n, 10)
		if err != nil {
			return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "amount overflow", apperr.ErrOverflow)
		}
		n3, err := addChecked(n2, digit)
		if err != nil {
			return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "amount overflow", apperr.ErrOverflow)
		}
		n = n3
	}
	return n, nil
}

const maxInt64 = int64(^uint64(0) >> 1)
const minInt64 = -maxInt64 - 1

func addChecked(a, b int64) (int64, error) {
	if b > 0 && a > maxInt64-b {
		return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "addition overflow", apperr.ErrOverflow)
	}
	if b < 0 && a < minInt64-b {
		return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "addition overflow", apperr.ErrOverflow)
	}
	return a + b, nil
}

func subChecked(a, b int64) (int64, error) {
	if b > 0 && a < minInt64+b {
		return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "subtraction overflow", apperr.ErrOverflow)
	}
	if b < 0 && a > maxInt64+b {
		return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "subtraction overflow", apperr.ErrOverflow)
	}
	return a - b, nil
}

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	if a == minInt64 || b == minInt64 {
		if a == 1 || b == 1 {
			return minInt64, nil
		}
		return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "multiplication overflow", apperr.ErrOverflow)
	}
	if a > 0 {
		if b > 0 {
			if a > maxInt64/b {
				return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "multiplication overflow", apperr.ErrOverflow)
			}
		} else if b < maxInt64/-a { // b negative
			return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "multiplication overflow", apperr.ErrOverflow)
		}
	} else { // a < 0
		if b > 0 {
			if a < minInt64/b {
				return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "multiplication overflow", apperr.ErrOverflow)
			}
		} else if a != 0 && b < maxInt64/a {
			return 0, apperr.WrapFailure(apperr.CodeInvalidAmount, "multiplication overflow", apperr.ErrOverflow)
		}
	}
	return a * b, nil
}
