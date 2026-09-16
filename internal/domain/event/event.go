package event

import (
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wallet"
)

type Type string

const (
	TypeWagerTransactionProcessed        Type = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         Type = "WagerTransactionRejected"
	TypeWalletBalanceChanged             Type = "WalletBalanceChanged"
	TypeWagerTransactionPendingReference Type = "WagerTransactionPendingReference"
)

// Envelope is the immutable integration event wrapper.
type Envelope struct {
	EventID       uuid.UUID
	EventType     Type
	AggregateID   uuid.UUID
	CorrelationID string
	CausationID   *uuid.UUID
	OccurredAt    time.Time
	Version       int
	Data          any
}

type MoneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func moneyDTO(m money.Money) MoneyDTO {
	return MoneyDTO{Amount: m.String(), Currency: m.Currency()}
}

type WagerTransactionProcessedData struct {
	TransactionID         uuid.UUID `json:"transactionId"`
	ProviderID            string    `json:"providerId,omitempty"`
	ExternalTransactionID string    `json:"externalTransactionId,omitempty"`
	WalletID              uuid.UUID `json:"walletId"`
	PlayerID              uuid.UUID `json:"playerId"`
	Kind                  string    `json:"kind"`
	Money                 MoneyDTO  `json:"money"`
	Status                string    `json:"status"`
	Balance               MoneyDTO  `json:"balance"`
}

type WagerTransactionRejectedData struct {
	TransactionID         uuid.UUID   `json:"transactionId"`
	ProviderID            string      `json:"providerId,omitempty"`
	ExternalTransactionID string      `json:"externalTransactionId,omitempty"`
	WalletID              uuid.UUID   `json:"walletId"`
	Kind                  string      `json:"kind"`
	FailureCode           apperr.Code `json:"failureCode"`
}

type WalletBalanceChangedData struct {
	WalletID      uuid.UUID        `json:"walletId"`
	TransactionID uuid.UUID        `json:"transactionId"`
	Direction     wallet.Direction `json:"direction"`
	Money         MoneyDTO         `json:"money"`
	BalanceBefore MoneyDTO         `json:"balanceBefore"`
	BalanceAfter  MoneyDTO         `json:"balanceAfter"`
	WalletVersion int64            `json:"walletVersion"`
}

type WagerTransactionPendingReferenceData struct {
	TransactionID                  uuid.UUID `json:"transactionId"`
	ProviderID                     string    `json:"providerId"`
	ExternalTransactionID          string    `json:"externalTransactionId"`
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId"`
	Kind                           string    `json:"kind"`
}

func NewWagerTransactionProcessed(
	eventID uuid.UUID,
	tx wagering.WagerTransaction,
	balance money.Money,
	correlationID string,
	causationID *uuid.UUID,
	occurredAt time.Time,
) (Envelope, error) {
	if eventID == uuid.Nil {
		return Envelope{}, apperr.WrapFailure(apperr.CodeInvalidInput, "event id required", apperr.ErrInvalidArgument)
	}
	return Envelope{
		EventID:       eventID,
		EventType:     TypeWagerTransactionProcessed,
		AggregateID:   tx.ID(),
		CorrelationID: correlationID,
		CausationID:   causationID,
		OccurredAt:    occurredAt.UTC(),
		Version:       1,
		Data: WagerTransactionProcessedData{
			TransactionID:         tx.ID(),
			ProviderID:            tx.ProviderID(),
			ExternalTransactionID: tx.ExternalTransactionID(),
			WalletID:              tx.WalletID(),
			PlayerID:              tx.PlayerID(),
			Kind:                  string(tx.Kind()),
			Money:                 moneyDTO(tx.Amount()),
			Status:                string(tx.Status()),
			Balance:               moneyDTO(balance),
		},
	}, nil
}

func NewWagerTransactionRejected(
	eventID uuid.UUID,
	tx wagering.WagerTransaction,
	correlationID string,
	causationID *uuid.UUID,
	occurredAt time.Time,
) (Envelope, error) {
	if eventID == uuid.Nil {
		return Envelope{}, apperr.WrapFailure(apperr.CodeInvalidInput, "event id required", apperr.ErrInvalidArgument)
	}
	return Envelope{
		EventID:       eventID,
		EventType:     TypeWagerTransactionRejected,
		AggregateID:   tx.ID(),
		CorrelationID: correlationID,
		CausationID:   causationID,
		OccurredAt:    occurredAt.UTC(),
		Version:       1,
		Data: WagerTransactionRejectedData{
			TransactionID:         tx.ID(),
			ProviderID:            tx.ProviderID(),
			ExternalTransactionID: tx.ExternalTransactionID(),
			WalletID:              tx.WalletID(),
			Kind:                  string(tx.Kind()),
			FailureCode:           tx.FailureCode(),
		},
	}, nil
}

func NewWalletBalanceChanged(
	eventID uuid.UUID,
	entry wallet.LedgerEntry,
	walletVersion int64,
	correlationID string,
	causationID *uuid.UUID,
	occurredAt time.Time,
) (Envelope, error) {
	if eventID == uuid.Nil {
		return Envelope{}, apperr.WrapFailure(apperr.CodeInvalidInput, "event id required", apperr.ErrInvalidArgument)
	}
	return Envelope{
		EventID:       eventID,
		EventType:     TypeWalletBalanceChanged,
		AggregateID:   entry.WalletID(),
		CorrelationID: correlationID,
		CausationID:   causationID,
		OccurredAt:    occurredAt.UTC(),
		Version:       1,
		Data: WalletBalanceChangedData{
			WalletID:      entry.WalletID(),
			TransactionID: entry.TransactionID(),
			Direction:     entry.Direction(),
			Money:         moneyDTO(entry.Amount()),
			BalanceBefore: moneyDTO(entry.BalanceBefore()),
			BalanceAfter:  moneyDTO(entry.BalanceAfter()),
			WalletVersion: walletVersion,
		},
	}, nil
}

func NewWagerTransactionPendingReference(
	eventID uuid.UUID,
	tx wagering.WagerTransaction,
	correlationID string,
	causationID *uuid.UUID,
	occurredAt time.Time,
) (Envelope, error) {
	if eventID == uuid.Nil {
		return Envelope{}, apperr.WrapFailure(apperr.CodeInvalidInput, "event id required", apperr.ErrInvalidArgument)
	}
	return Envelope{
		EventID:       eventID,
		EventType:     TypeWagerTransactionPendingReference,
		AggregateID:   tx.ID(),
		CorrelationID: correlationID,
		CausationID:   causationID,
		OccurredAt:    occurredAt.UTC(),
		Version:       1,
		Data: WagerTransactionPendingReferenceData{
			TransactionID:                  tx.ID(),
			ProviderID:                     tx.ProviderID(),
			ExternalTransactionID:          tx.ExternalTransactionID(),
			ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
			Kind:                           string(tx.Kind()),
		},
	}, nil
}
