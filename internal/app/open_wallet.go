package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/event"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wallet"
)

type OpenWalletInput struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money
	CorrelationID  string
}

type OpenWalletResult struct {
	Wallet wallet.Wallet
}

type OpenWallet struct {
	uow UnitOfWork
}

func NewOpenWallet(uow UnitOfWork) *OpenWallet {
	return &OpenWallet{uow: uow}
}

func (uc *OpenWallet) Execute(ctx context.Context, in OpenWalletInput) (OpenWalletResult, error) {
	if in.PlayerID == uuid.Nil {
		return OpenWalletResult{}, apperr.WrapFailure(apperr.CodeInvalidInput, "playerId required", apperr.ErrInvalidArgument)
	}
	if in.InitialBalance.IsNegative() {
		return OpenWalletResult{}, apperr.NewFailure(apperr.CodeInvalidAmount, "initial balance cannot be negative")
	}

	var result OpenWalletResult
	err := uc.uow.WithinTransaction(ctx, func(ctx context.Context, repos Repositories) error {
		now := time.Now().UTC()
		walletID, err := uuid.NewV7()
		if err != nil {
			walletID = uuid.New()
		}
		w, err := wallet.Create(walletID, in.PlayerID, in.InitialBalance, now)
		if err != nil {
			return err
		}
		if err := repos.Wallets().Insert(ctx, w); err != nil {
			return apperr.WrapFailure(apperr.CodeConflict, "wallet already exists for player/currency", err)
		}

		if in.InitialBalance.IsPositive() {
			txID, err := uuid.NewV7()
			if err != nil {
				txID = uuid.New()
			}
			opening, err := wagering.NewOpening(wagering.NewOpeningParams{
				ID: txID, WalletID: w.ID(), PlayerID: w.PlayerID(), Amount: in.InitialBalance, Now: now,
			})
			if err != nil {
				return err
			}
			opening, err = opening.MarkProcessed(in.InitialBalance, nil, now)
			if err != nil {
				return err
			}
			if err := repos.Transactions().Insert(ctx, opening); err != nil {
				return err
			}

			entryID, err := uuid.NewV7()
			if err != nil {
				entryID = uuid.New()
			}
			zero, err := money.Zero(in.InitialBalance.Currency())
			if err != nil {
				return err
			}
			entry, err := wallet.NewLedgerEntry(
				entryID, w.ID(), opening.ID(), wallet.DirectionCredit,
				in.InitialBalance, zero, in.InitialBalance, now,
			)
			if err != nil {
				return err
			}
			if err := repos.Ledger().Insert(ctx, entry); err != nil {
				return err
			}

			ev1, err := uuid.NewV7()
			if err != nil {
				ev1 = uuid.New()
			}
			processed, err := event.NewWagerTransactionProcessed(ev1, opening, in.InitialBalance, in.CorrelationID, nil, now)
			if err != nil {
				return err
			}
			if err := repos.Outbox().Insert(ctx, processed); err != nil {
				return err
			}

			ev2, err := uuid.NewV7()
			if err != nil {
				ev2 = uuid.New()
			}
			changed, err := event.NewWalletBalanceChanged(ev2, entry, w.Version(), in.CorrelationID, nil, now)
			if err != nil {
				return err
			}
			if err := repos.Outbox().Insert(ctx, changed); err != nil {
				return err
			}
		}

		result.Wallet = w
		return nil
	})
	return result, err
}
