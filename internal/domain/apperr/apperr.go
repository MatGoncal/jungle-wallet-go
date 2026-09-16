package apperr

import (
	"errors"
	"fmt"
)

// Sentinel errors for classification via errors.Is.
var (
	ErrInvalidArgument    = errors.New("invalid argument")
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("conflict")
	ErrInsufficientFunds  = errors.New("insufficient funds")
	ErrCurrencyMismatch   = errors.New("currency mismatch")
	ErrInvalidTransition  = errors.New("invalid state transition")
	ErrInvariantViolation = errors.New("invariant violation")
	ErrOverflow           = errors.New("numeric overflow")
	ErrKindNotAllowed     = errors.New("kind not allowed")
	ErrReference          = errors.New("reference error")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrForbidden          = errors.New("forbidden")
	ErrTransient          = errors.New("transient failure")
)

// Code is a stable failure code returned to clients.
type Code string

const (
	CodeInsufficientFunds         Code = "INSUFFICIENT_FUNDS"
	CodeReversalInsufficientFunds Code = "REVERSAL_INSUFFICIENT_FUNDS"
	CodeReferenceNotFound         Code = "REFERENCE_NOT_FOUND"
	CodeReferenceAlreadyReversed  Code = "REFERENCE_ALREADY_REVERSED"
	CodeReferenceNotProcessed     Code = "REFERENCE_NOT_PROCESSED"
	CodeReferenceMismatch         Code = "REFERENCE_MISMATCH"
	CodeReferenceAmountMismatch   Code = "REFERENCE_AMOUNT_MISMATCH"
	CodeReferenceNotReversible    Code = "REFERENCE_NOT_REVERSIBLE"
	CodeCurrencyMismatch          Code = "CURRENCY_MISMATCH"
	CodeInvalidAmount             Code = "INVALID_AMOUNT"
	CodeKindNotAllowed            Code = "KIND_NOT_ALLOWED"
	CodeWalletNotFound            Code = "WALLET_NOT_FOUND"
	CodePlayerWalletMismatch      Code = "PLAYER_WALLET_MISMATCH"
	CodeConflict                  Code = "CONFLICT"
	CodeInvalidInput              Code = "INVALID_INPUT"
)

// Failure is a domain rejection carrying a stable failure code.
type Failure struct {
	Code    Code
	Message string
	Cause   error
}

func (f *Failure) Error() string {
	if f.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", f.Code, f.Message, f.Cause)
	}
	return fmt.Sprintf("%s: %s", f.Code, f.Message)
}

func (f *Failure) Unwrap() error {
	if f.Cause != nil {
		return f.Cause
	}
	switch f.Code {
	case CodeInsufficientFunds, CodeReversalInsufficientFunds:
		return ErrInsufficientFunds
	case CodeCurrencyMismatch:
		return ErrCurrencyMismatch
	case CodeKindNotAllowed:
		return ErrKindNotAllowed
	case CodeWalletNotFound, CodePlayerWalletMismatch:
		return ErrNotFound
	case CodeConflict:
		return ErrConflict
	case CodeReferenceNotFound, CodeReferenceAlreadyReversed, CodeReferenceNotProcessed,
		CodeReferenceMismatch, CodeReferenceAmountMismatch, CodeReferenceNotReversible:
		return ErrReference
	case CodeInvalidAmount, CodeInvalidInput:
		return ErrInvalidArgument
	default:
		return ErrInvariantViolation
	}
}

func NewFailure(code Code, message string) *Failure {
	return &Failure{Code: code, Message: message}
}

func WrapFailure(code Code, message string, cause error) *Failure {
	return &Failure{Code: code, Message: message, Cause: cause}
}

// AsFailure extracts a *Failure from an error chain.
func AsFailure(err error) (*Failure, bool) {
	var f *Failure
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}
