package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/event"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
)

// ResolvePendingReference retries a PENDING_REFERENCE transaction.
type ResolvePendingReference struct {
	uow UnitOfWork
}

func NewResolvePendingReference(uow UnitOfWork) *ResolvePendingReference {
	return &ResolvePendingReference{uow: uow}
}

type ResolvePendingInput struct {
	TransactionID        uuid.UUID
	CorrelationID        string
	ReferenceMaxAttempts int
}

func (uc *ResolvePendingReference) Execute(ctx context.Context, in ResolvePendingInput) error {
	maxAttempts := in.ReferenceMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultReferenceMaxTries
	}

	return uc.uow.WithinTransaction(ctx, func(ctx context.Context, repos Repositories) error {
		tx, err := repos.Transactions().GetByID(ctx, in.TransactionID)
		if err != nil {
			return err
		}
		if tx.Status() != wagering.StatusPendingReference {
			return nil
		}

		retry, ok, err := repos.ReferenceRetries().Get(ctx, tx.ID())
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if !ok {
			return uc.rejectNotFound(ctx, repos, tx, in.CorrelationID, now)
		}

		w, err := repos.Wallets().GetForUpdate(ctx, tx.WalletID())
		if err != nil {
			return err
		}

		ref, found, err := repos.Transactions().GetByExternalID(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID())
		if err != nil {
			return err
		}
		if !found || ref.Status() == wagering.StatusPendingReference || ref.Status() == wagering.StatusPending {
			if now.After(retry.ExpiresAt) || retry.Attempts+1 >= maxAttempts {
				return uc.rejectNotFound(ctx, repos, tx, in.CorrelationID, now)
			}
			return uc.scheduleRetry(ctx, repos, retry, now)
		}
		if ref.Status() == wagering.StatusRejected || ref.Status() == wagering.StatusFailed {
			return uc.rejectWithCode(ctx, repos, tx, w.Balance().AmountMinor(), apperr.CodeReferenceNotProcessed, in.CorrelationID, now)
		}
		if ref.Status() != wagering.StatusProcessed {
			return uc.scheduleRetry(ctx, repos, retry, now)
		}

		if err := validateReferenceMatch(tx, ref); err != nil {
			if f, ok := apperr.AsFailure(err); ok {
				return uc.rejectWithCode(ctx, repos, tx, w.Balance().AmountMinor(), f.Code, in.CorrelationID, now)
			}
			return err
		}
		if _, already, err := repos.Transactions().GetProcessedReversalByReference(ctx, ref.ID()); err != nil {
			return err
		} else if already {
			return uc.rejectWithCode(ctx, repos, tx, w.Balance().AmountMinor(), apperr.CodeReferenceAlreadyReversed, in.CorrelationID, now)
		}

		pwIn := ProcessWagerInput{
			ProviderID:                     tx.ProviderID(),
			ExternalTransactionID:          tx.ExternalTransactionID(),
			IdempotencyKey:                 tx.IdempotencyKey(),
			PlayerID:                       tx.PlayerID(),
			WalletID:                       tx.WalletID(),
			RoundID:                        tx.RoundID(),
			GameID:                         tx.GameID(),
			Kind:                           tx.Kind(),
			Money:                          tx.Amount(),
			ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
			CorrelationID:                  in.CorrelationID,
		}
		proc := &ProcessWagerTransaction{}
		refID := ref.ID()
		var result ProcessWagerResult
		switch ref.Kind() {
		case wagering.KindBet:
			return proc.applyCreditWithRef(ctx, repos, tx, w, pwIn, &refID, now, &result)
		case wagering.KindWin:
			if tx.Kind() != wagering.KindRollback {
				return uc.rejectWithCode(ctx, repos, tx, w.Balance().AmountMinor(), apperr.CodeReferenceNotReversible, in.CorrelationID, now)
			}
			return proc.applyDebitWithRef(ctx, repos, tx, w, pwIn, &refID, now, apperr.CodeReversalInsufficientFunds, &result)
		default:
			return uc.rejectWithCode(ctx, repos, tx, w.Balance().AmountMinor(), apperr.CodeReferenceNotReversible, in.CorrelationID, now)
		}
	})
}

func (uc *ResolvePendingReference) scheduleRetry(
	ctx context.Context,
	repos Repositories,
	retry ReferenceRetry,
	now time.Time,
) error {
	attempts := retry.Attempts + 1
	shift := attempts
	if shift > 8 {
		shift = 8
	}
	backoff := time.Second << shift
	next := now.Add(backoff)
	return repos.ReferenceRetries().Upsert(ctx, retry.TransactionID, attempts, next, retry.ExpiresAt, now)
}

func (uc *ResolvePendingReference) rejectNotFound(
	ctx context.Context,
	repos Repositories,
	tx wagering.WagerTransaction,
	correlationID string,
	now time.Time,
) error {
	w, err := repos.Wallets().Get(ctx, tx.WalletID())
	if err != nil {
		return err
	}
	return uc.rejectWithCode(ctx, repos, tx, w.Balance().AmountMinor(), apperr.CodeReferenceNotFound, correlationID, now)
}

func (uc *ResolvePendingReference) rejectWithCode(
	ctx context.Context,
	repos Repositories,
	tx wagering.WagerTransaction,
	balanceMinor int64,
	code apperr.Code,
	correlationID string,
	now time.Time,
) error {
	rejected, err := tx.MarkRejected(code, now)
	if err != nil {
		return err
	}
	rejected = setResultBalance(rejected, balanceMinor)
	if err := repos.Transactions().Update(ctx, rejected); err != nil {
		return err
	}
	if err := repos.ReferenceRetries().Delete(ctx, rejected.ID()); err != nil {
		return err
	}
	evID, err := newID()
	if err != nil {
		return err
	}
	env, err := event.NewWagerTransactionRejected(evID, rejected, correlationID, nil, now)
	if err != nil {
		return err
	}
	return repos.Outbox().Insert(ctx, env)
}
