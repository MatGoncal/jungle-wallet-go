package wagering

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
)

type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

func (s Status) IsTerminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}

// IdempotencyInput is the business payload hashed for conflict detection.
type IdempotencyInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
}

// CanonicalHash returns SHA-256 hex of canonical JSON (sorted keys, no spaces, no nulls).
// Excludes Idempotency-Key and transport metadata. Same for HTTP and SQS.
func CanonicalHash(in IdempotencyInput) (string, error) {
	payload := map[string]any{
		"providerId":            in.ProviderID,
		"externalTransactionId": in.ExternalTransactionID,
		"playerId":              in.PlayerID,
		"walletId":              in.WalletID,
		"roundId":               in.RoundID,
		"gameId":                in.GameID,
		"kind":                  string(in.Kind),
		"money": map[string]any{
			"amount":   in.Money.AmountMinor(),
			"currency": in.Money.Currency(),
		},
	}
	if in.ReferenceExternalTransactionID != "" {
		payload["referenceExternalTransactionId"] = in.ReferenceExternalTransactionID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// WagerTransaction is the domain entity for financial operations.
type WagerTransaction struct {
	id                             uuid.UUID
	providerID                     string
	externalTransactionID          string
	idempotencyKey                 string
	payloadHash                    string
	walletID                       uuid.UUID
	playerID                       uuid.UUID
	roundID                        string
	gameID                         string
	kind                           Kind
	amount                         money.Money
	referenceExternalTransactionID string
	referenceTransactionID         *uuid.UUID
	status                         Status
	failureCode                    apperr.Code
	resultBalanceMinor             *int64
	createdAt                      time.Time
	updatedAt                      time.Time
	origin                         Origin
}

type Origin string

const (
	OriginExternal Origin = "EXTERNAL"
	OriginInternal Origin = "INTERNAL"
)

type NewExternalParams struct {
	ID                             uuid.UUID
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	WalletID                       uuid.UUID
	PlayerID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Amount                         money.Money
	ReferenceExternalTransactionID string
	Now                            time.Time
}

func NewExternal(p NewExternalParams) (WagerTransaction, error) {
	if p.Kind == KindOpening {
		return WagerTransaction{}, apperr.NewFailure(apperr.CodeKindNotAllowed, "OPENING is reserved for internal wallet opening")
	}
	if err := validateExternalKindAmount(p.Kind, p.Amount); err != nil {
		return WagerTransaction{}, err
	}
	if p.ID == uuid.Nil || p.WalletID == uuid.Nil || p.PlayerID == uuid.Nil {
		return WagerTransaction{}, apperr.WrapFailure(apperr.CodeInvalidInput, "ids required", apperr.ErrInvalidArgument)
	}
	if p.ProviderID == "" || p.ExternalTransactionID == "" || p.IdempotencyKey == "" || p.PayloadHash == "" {
		return WagerTransaction{}, apperr.WrapFailure(apperr.CodeInvalidInput, "provider identity fields required", apperr.ErrInvalidArgument)
	}
	if p.RoundID == "" || p.GameID == "" {
		return WagerTransaction{}, apperr.WrapFailure(apperr.CodeInvalidInput, "round and game required", apperr.ErrInvalidArgument)
	}
	if needsReference(p.Kind) && p.ReferenceExternalTransactionID == "" {
		return WagerTransaction{}, apperr.WrapFailure(apperr.CodeInvalidInput, "referenceExternalTransactionId required", apperr.ErrInvalidArgument)
	}
	now := p.Now.UTC()
	return WagerTransaction{
		id:                             p.ID,
		providerID:                     p.ProviderID,
		externalTransactionID:          p.ExternalTransactionID,
		idempotencyKey:                 p.IdempotencyKey,
		payloadHash:                    p.PayloadHash,
		walletID:                       p.WalletID,
		playerID:                       p.PlayerID,
		roundID:                        p.RoundID,
		gameID:                         p.GameID,
		kind:                           p.Kind,
		amount:                         p.Amount,
		referenceExternalTransactionID: p.ReferenceExternalTransactionID,
		status:                         StatusPending,
		createdAt:                      now,
		updatedAt:                      now,
		origin:                         OriginExternal,
	}, nil
}

type NewOpeningParams struct {
	ID       uuid.UUID
	WalletID uuid.UUID
	PlayerID uuid.UUID
	Amount   money.Money
	Now      time.Time
}

func NewOpening(p NewOpeningParams) (WagerTransaction, error) {
	if p.ID == uuid.Nil || p.WalletID == uuid.Nil || p.PlayerID == uuid.Nil {
		return WagerTransaction{}, apperr.WrapFailure(apperr.CodeInvalidInput, "ids required", apperr.ErrInvalidArgument)
	}
	if !p.Amount.IsPositive() {
		return WagerTransaction{}, apperr.NewFailure(apperr.CodeInvalidAmount, "OPENING requires positive amount")
	}
	now := p.Now.UTC()
	return WagerTransaction{
		id:        p.ID,
		walletID:  p.WalletID,
		playerID:  p.PlayerID,
		kind:      KindOpening,
		amount:    p.Amount,
		status:    StatusPending,
		createdAt: now,
		updatedAt: now,
		origin:    OriginInternal,
	}, nil
}

// PersistState is the durable snapshot used for reidratação.
type PersistState struct {
	ID                             uuid.UUID
	Origin                         Origin
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	WalletID                       uuid.UUID
	PlayerID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Amount                         money.Money
	ReferenceExternalTransactionID string
	ReferenceTransactionID         *uuid.UUID
	Status                         Status
	FailureCode                    apperr.Code
	ResultBalanceMinor             *int64
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
}

// Rehydrate rebuilds from persistence without reapplying transitions or emitting events.
func Rehydrate(s PersistState) (WagerTransaction, error) {
	if s.ID == uuid.Nil || s.WalletID == uuid.Nil || s.PlayerID == uuid.Nil {
		return WagerTransaction{}, apperr.WrapFailure(apperr.CodeInvalidInput, "ids required", apperr.ErrInvalidArgument)
	}
	if s.Kind == "" || s.Status == "" || s.Origin == "" {
		return WagerTransaction{}, apperr.WrapFailure(apperr.CodeInvalidInput, "kind/status/origin required", apperr.ErrInvalidArgument)
	}
	return WagerTransaction{
		id:                             s.ID,
		providerID:                     s.ProviderID,
		externalTransactionID:          s.ExternalTransactionID,
		idempotencyKey:                 s.IdempotencyKey,
		payloadHash:                    s.PayloadHash,
		walletID:                       s.WalletID,
		playerID:                       s.PlayerID,
		roundID:                        s.RoundID,
		gameID:                         s.GameID,
		kind:                           s.Kind,
		amount:                         s.Amount,
		referenceExternalTransactionID: s.ReferenceExternalTransactionID,
		referenceTransactionID:         s.ReferenceTransactionID,
		status:                         s.Status,
		failureCode:                    s.FailureCode,
		resultBalanceMinor:             s.ResultBalanceMinor,
		createdAt:                      s.CreatedAt.UTC(),
		updatedAt:                      s.UpdatedAt.UTC(),
		origin:                         s.Origin,
	}, nil
}

func validateExternalKindAmount(kind Kind, amount money.Money) error {
	switch kind {
	case KindBet, KindWin, KindRefund, KindRollback:
		if !amount.IsPositive() {
			return apperr.NewFailure(apperr.CodeInvalidAmount, fmt.Sprintf("%s requires amount > 0", kind))
		}
	case KindLoss:
		if !amount.IsZero() {
			return apperr.NewFailure(apperr.CodeInvalidAmount, "LOSS requires amount 0.00")
		}
	default:
		return apperr.NewFailure(apperr.CodeKindNotAllowed, fmt.Sprintf("unknown kind %s", kind))
	}
	return nil
}

func needsReference(kind Kind) bool {
	return kind == KindRefund || kind == KindRollback
}

func (t WagerTransaction) ID() uuid.UUID                 { return t.id }
func (t WagerTransaction) ProviderID() string            { return t.providerID }
func (t WagerTransaction) ExternalTransactionID() string { return t.externalTransactionID }
func (t WagerTransaction) IdempotencyKey() string        { return t.idempotencyKey }
func (t WagerTransaction) PayloadHash() string           { return t.payloadHash }
func (t WagerTransaction) WalletID() uuid.UUID           { return t.walletID }
func (t WagerTransaction) PlayerID() uuid.UUID           { return t.playerID }
func (t WagerTransaction) RoundID() string               { return t.roundID }
func (t WagerTransaction) GameID() string                { return t.gameID }
func (t WagerTransaction) Kind() Kind                    { return t.kind }
func (t WagerTransaction) Amount() money.Money           { return t.amount }
func (t WagerTransaction) ReferenceExternalTransactionID() string {
	return t.referenceExternalTransactionID
}
func (t WagerTransaction) ReferenceTransactionID() *uuid.UUID { return t.referenceTransactionID }
func (t WagerTransaction) Status() Status                     { return t.status }
func (t WagerTransaction) FailureCode() apperr.Code           { return t.failureCode }
func (t WagerTransaction) ResultBalanceMinor() *int64         { return t.resultBalanceMinor }
func (t WagerTransaction) CreatedAt() time.Time               { return t.createdAt }
func (t WagerTransaction) UpdatedAt() time.Time               { return t.updatedAt }
func (t WagerTransaction) Origin() Origin                     { return t.origin }

func (t WagerTransaction) MarkPendingReference(now time.Time) (WagerTransaction, error) {
	if err := t.ensureTransition(StatusPendingReference); err != nil {
		return WagerTransaction{}, err
	}
	t.status = StatusPendingReference
	t.updatedAt = now.UTC()
	return t, nil
}

func (t WagerTransaction) MarkProcessed(resultBalance money.Money, refID *uuid.UUID, now time.Time) (WagerTransaction, error) {
	if err := t.ensureTransition(StatusProcessed); err != nil {
		return WagerTransaction{}, err
	}
	if resultBalance.Currency() != t.amount.Currency() {
		return WagerTransaction{}, apperr.NewFailure(apperr.CodeCurrencyMismatch, "result balance currency mismatch")
	}
	minor := resultBalance.AmountMinor()
	t.status = StatusProcessed
	t.resultBalanceMinor = &minor
	t.referenceTransactionID = refID
	t.updatedAt = now.UTC()
	return t, nil
}

func (t WagerTransaction) MarkRejected(code apperr.Code, now time.Time) (WagerTransaction, error) {
	if err := t.ensureTransition(StatusRejected); err != nil {
		return WagerTransaction{}, err
	}
	if code == "" {
		return WagerTransaction{}, apperr.WrapFailure(apperr.CodeInvalidInput, "failure code required", apperr.ErrInvalidArgument)
	}
	t.status = StatusRejected
	t.failureCode = code
	t.updatedAt = now.UTC()
	return t, nil
}

func (t WagerTransaction) MarkFailed(code apperr.Code, now time.Time) (WagerTransaction, error) {
	if err := t.ensureTransition(StatusFailed); err != nil {
		return WagerTransaction{}, err
	}
	t.status = StatusFailed
	t.failureCode = code
	t.updatedAt = now.UTC()
	return t, nil
}

func (t WagerTransaction) ensureTransition(to Status) error {
	if t.status.IsTerminal() {
		return apperr.WrapFailure(apperr.CodeInvalidInput, "terminal transaction cannot transition", apperr.ErrInvalidTransition)
	}
	allowed := map[Status]map[Status]bool{
		StatusPending: {
			StatusPendingReference: true,
			StatusProcessed:        true,
			StatusRejected:         true,
			StatusFailed:           true,
		},
		StatusPendingReference: {
			StatusProcessed: true,
			StatusRejected:  true,
			StatusFailed:    true,
		},
	}
	if !allowed[t.status][to] {
		return apperr.WrapFailure(
			apperr.CodeInvalidInput,
			fmt.Sprintf("cannot transition %s -> %s", t.status, to),
			apperr.ErrInvalidTransition,
		)
	}
	return nil
}
