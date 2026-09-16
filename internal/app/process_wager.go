package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/event"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wallet"
)

const (
	DefaultReferenceTTL      = time.Hour
	DefaultReferenceMaxTries = 12
)

type ProcessWagerInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           wagering.Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	CorrelationID                  string
	CausationID                    *uuid.UUID
	ReferenceTTL                   time.Duration
	ReferenceMaxAttempts           int
}

type ProcessWagerResult struct {
	Transaction      wagering.WagerTransaction
	Balance          money.Money
	IdempotentReplay bool
}

type ProcessWagerTransaction struct {
	uow UnitOfWork
}

func NewProcessWagerTransaction(uow UnitOfWork) *ProcessWagerTransaction {
	return &ProcessWagerTransaction{uow: uow}
}

func (uc *ProcessWagerTransaction) Execute(ctx context.Context, in ProcessWagerInput) (ProcessWagerResult, error) {
	pending, hash, ttl, maxAttempts, now, err := uc.prepare(in)
	if err != nil {
		return ProcessWagerResult{}, err
	}
	var result ProcessWagerResult
	err = uc.uow.WithinTransaction(ctx, func(ctx context.Context, repos Repositories) error {
		var err error
		result, err = uc.ExecuteInTx(ctx, repos, in, pending, hash, ttl, maxAttempts, now)
		return err
	})
	return result, err
}

// ExecuteInTx runs the use case against an already-open UnitOfWork transaction
// (shared with inbox insert on the SQS path).
func (uc *ProcessWagerTransaction) ExecuteInTx(
	ctx context.Context,
	repos Repositories,
	in ProcessWagerInput,
	pending wagering.WagerTransaction,
	hash string,
	ttl time.Duration,
	maxAttempts int,
	now time.Time,
) (ProcessWagerResult, error) {
	var result ProcessWagerResult
	if existingExt, ok, err := repos.Transactions().GetByExternalID(ctx, in.ProviderID, in.ExternalTransactionID); err != nil {
		return result, err
	} else if ok && existingExt.IdempotencyKey() != in.IdempotencyKey {
		return result, apperr.WrapFailure(
			apperr.CodeConflict,
			"externalTransactionId already processed with a different idempotency key",
			apperr.ErrConflict,
		)
	}

	inserted, existing, err := repos.Transactions().TryInsertIdempotency(ctx, pending)
	if err != nil {
		return result, mapInsertConflict(err)
	}
	if !inserted {
		if err := uc.replayOrConflict(ctx, repos, existing, hash, &result); err != nil {
			return result, err
		}
		return result, nil
	}

	w, err := repos.Wallets().GetForUpdate(ctx, in.WalletID)
	if err != nil {
		return result, err
	}
	if w.PlayerID() != in.PlayerID {
		if err := rejectAndPersist(ctx, repos, pending, w.Balance(), apperr.CodePlayerWalletMismatch, in, now, &result); err != nil {
			return result, err
		}
		return result, nil
	}
	if w.Currency() != in.Money.Currency() {
		if err := rejectAndPersist(ctx, repos, pending, w.Balance(), apperr.CodeCurrencyMismatch, in, now, &result); err != nil {
			return result, err
		}
		return result, nil
	}

	if in.Kind == wagering.KindRefund || in.Kind == wagering.KindRollback {
		if err := uc.processReversal(ctx, repos, pending, w, in, ttl, maxAttempts, now, &result); err != nil {
			return result, err
		}
		return result, nil
	}
	if err := uc.processSimple(ctx, repos, pending, w, in, now, &result); err != nil {
		return result, err
	}
	return result, nil
}

// ExecuteWithRepos is used by the SQS consumer inside an open transaction (inbox + domain).
func (uc *ProcessWagerTransaction) ExecuteWithRepos(ctx context.Context, repos Repositories, in ProcessWagerInput) (ProcessWagerResult, error) {
	pending, hash, ttl, maxAttempts, now, err := uc.prepare(in)
	if err != nil {
		return ProcessWagerResult{}, err
	}
	return uc.ExecuteInTx(ctx, repos, in, pending, hash, ttl, maxAttempts, now)
}

func (uc *ProcessWagerTransaction) prepare(in ProcessWagerInput) (wagering.WagerTransaction, string, time.Duration, int, time.Time, error) {
	if in.IdempotencyKey == "" {
		return wagering.WagerTransaction{}, "", 0, 0, time.Time{}, apperr.WrapFailure(apperr.CodeInvalidInput, "Idempotency-Key required", apperr.ErrInvalidArgument)
	}
	if in.Kind == wagering.KindOpening {
		return wagering.WagerTransaction{}, "", 0, 0, time.Time{}, apperr.NewFailure(apperr.CodeKindNotAllowed, "OPENING is not allowed via external channels")
	}

	hash, err := wagering.CanonicalHash(wagering.IdempotencyInput{
		ProviderID:                     in.ProviderID,
		ExternalTransactionID:          in.ExternalTransactionID,
		PlayerID:                       in.PlayerID.String(),
		WalletID:                       in.WalletID.String(),
		RoundID:                        in.RoundID,
		GameID:                         in.GameID,
		Kind:                           in.Kind,
		Money:                          in.Money,
		ReferenceExternalTransactionID: in.ReferenceExternalTransactionID,
	})
	if err != nil {
		return wagering.WagerTransaction{}, "", 0, 0, time.Time{}, err
	}

	txID, err := uuid.NewV7()
	if err != nil {
		txID = uuid.New()
	}
	now := time.Now().UTC()
	pending, err := wagering.NewExternal(wagering.NewExternalParams{
		ID:                             txID,
		ProviderID:                     in.ProviderID,
		ExternalTransactionID:          in.ExternalTransactionID,
		IdempotencyKey:                 in.IdempotencyKey,
		PayloadHash:                    hash,
		WalletID:                       in.WalletID,
		PlayerID:                       in.PlayerID,
		RoundID:                        in.RoundID,
		GameID:                         in.GameID,
		Kind:                           in.Kind,
		Amount:                         in.Money,
		ReferenceExternalTransactionID: in.ReferenceExternalTransactionID,
		Now:                            now,
	})
	if err != nil {
		return wagering.WagerTransaction{}, "", 0, 0, time.Time{}, err
	}

	ttl := in.ReferenceTTL
	if ttl <= 0 {
		ttl = DefaultReferenceTTL
	}
	maxAttempts := in.ReferenceMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultReferenceMaxTries
	}
	return pending, hash, ttl, maxAttempts, now, nil
}

func (uc *ProcessWagerTransaction) replayOrConflict(
	ctx context.Context,
	repos Repositories,
	existing wagering.WagerTransaction,
	hash string,
	result *ProcessWagerResult,
) error {
	if existing.PayloadHash() != hash {
		return apperr.WrapFailure(apperr.CodeConflict, "idempotency key reused with different payload", apperr.ErrConflict)
	}
	bal, err := snapshotBalance(existing)
	if err != nil {
		w, werr := repos.Wallets().Get(ctx, existing.WalletID())
		if werr != nil {
			return werr
		}
		bal = w.Balance()
	}
	result.Transaction = existing
	result.Balance = bal
	result.IdempotentReplay = true
	return nil
}

func (uc *ProcessWagerTransaction) processSimple(
	ctx context.Context,
	repos Repositories,
	tx wagering.WagerTransaction,
	w wallet.Wallet,
	in ProcessWagerInput,
	now time.Time,
	result *ProcessWagerResult,
) error {
	switch tx.Kind() {
	case wagering.KindBet:
		return uc.applyDebit(ctx, repos, tx, w, in, now, apperr.CodeInsufficientFunds, result)
	case wagering.KindWin:
		return uc.applyCredit(ctx, repos, tx, w, in, now, result)
	case wagering.KindLoss:
		return uc.applyLoss(ctx, repos, tx, w, in, now, result)
	default:
		return apperr.NewFailure(apperr.CodeKindNotAllowed, fmt.Sprintf("unsupported kind %s", tx.Kind()))
	}
}

func (uc *ProcessWagerTransaction) processReversal(
	ctx context.Context,
	repos Repositories,
	tx wagering.WagerTransaction,
	w wallet.Wallet,
	in ProcessWagerInput,
	ttl time.Duration,
	maxAttempts int,
	now time.Time,
	result *ProcessWagerResult,
) error {
	ref, ok, err := repos.Transactions().GetByExternalID(ctx, in.ProviderID, in.ReferenceExternalTransactionID)
	if err != nil {
		return err
	}
	if !ok {
		return uc.parkPendingReference(ctx, repos, tx, w, in, ttl, maxAttempts, now, result)
	}
	if ref.Status() == wagering.StatusPendingReference {
		return uc.parkPendingReference(ctx, repos, tx, w, in, ttl, maxAttempts, now, result)
	}
	if ref.Status() == wagering.StatusRejected || ref.Status() == wagering.StatusFailed {
		return rejectAndPersist(ctx, repos, tx, w.Balance(), apperr.CodeReferenceNotProcessed, in, now, result)
	}
	if ref.Status() != wagering.StatusProcessed {
		return uc.parkPendingReference(ctx, repos, tx, w, in, ttl, maxAttempts, now, result)
	}

	if err := validateReferenceMatch(tx, ref); err != nil {
		var f *apperr.Failure
		if errors.As(err, &f) {
			return rejectAndPersist(ctx, repos, tx, w.Balance(), f.Code, in, now, result)
		}
		return err
	}

	if _, already, err := repos.Transactions().GetProcessedReversalByReference(ctx, ref.ID()); err != nil {
		return err
	} else if already {
		return rejectAndPersist(ctx, repos, tx, w.Balance(), apperr.CodeReferenceAlreadyReversed, in, now, result)
	}

	refID := ref.ID()
	switch ref.Kind() {
	case wagering.KindBet:
		return uc.applyCreditWithRef(ctx, repos, tx, w, in, &refID, now, result)
	case wagering.KindWin:
		if tx.Kind() != wagering.KindRollback {
			return rejectAndPersist(ctx, repos, tx, w.Balance(), apperr.CodeReferenceNotReversible, in, now, result)
		}
		return uc.applyDebitWithRef(ctx, repos, tx, w, in, &refID, now, apperr.CodeReversalInsufficientFunds, result)
	default:
		return rejectAndPersist(ctx, repos, tx, w.Balance(), apperr.CodeReferenceNotReversible, in, now, result)
	}
}

func (uc *ProcessWagerTransaction) parkPendingReference(
	ctx context.Context,
	repos Repositories,
	tx wagering.WagerTransaction,
	w wallet.Wallet,
	in ProcessWagerInput,
	ttl time.Duration,
	maxAttempts int,
	now time.Time,
	result *ProcessWagerResult,
) error {
	_ = maxAttempts
	parked, err := tx.MarkPendingReference(now)
	if err != nil {
		return err
	}
	if err := repos.Transactions().Update(ctx, parked); err != nil {
		return err
	}
	evID, err := newID()
	if err != nil {
		return err
	}
	env, err := event.NewWagerTransactionPendingReference(evID, parked, in.CorrelationID, in.CausationID, now)
	if err != nil {
		return err
	}
	if err := repos.Outbox().Insert(ctx, env); err != nil {
		return err
	}
	if err := repos.ReferenceRetries().Upsert(ctx, parked.ID(), 0, now.Add(time.Second), now.Add(ttl), now); err != nil {
		return err
	}
	result.Transaction = parked
	result.Balance = w.Balance()
	result.IdempotentReplay = false
	return nil
}

func (uc *ProcessWagerTransaction) applyDebit(
	ctx context.Context,
	repos Repositories,
	tx wagering.WagerTransaction,
	w wallet.Wallet,
	in ProcessWagerInput,
	now time.Time,
	fundsCode apperr.Code,
	result *ProcessWagerResult,
) error {
	return uc.applyDebitWithRef(ctx, repos, tx, w, in, nil, now, fundsCode, result)
}

func (uc *ProcessWagerTransaction) applyDebitWithRef(
	ctx context.Context,
	repos Repositories,
	tx wagering.WagerTransaction,
	w wallet.Wallet,
	in ProcessWagerInput,
	refID *uuid.UUID,
	now time.Time,
	fundsCode apperr.Code,
	result *ProcessWagerResult,
) error {
	entryID, err := newID()
	if err != nil {
		return err
	}
	expectedVersion := w.Version()
	mov, err := w.Debit(entryID, tx.ID(), tx.Amount(), now)
	if err != nil {
		if f, ok := apperr.AsFailure(err); ok && f.Code == apperr.CodeInsufficientFunds {
			return rejectAndPersist(ctx, repos, tx, w.Balance(), fundsCode, in, now, result)
		}
		return err
	}
	return uc.commitMovement(ctx, repos, tx, mov, expectedVersion, refID, in, now, result)
}

func (uc *ProcessWagerTransaction) applyCredit(
	ctx context.Context,
	repos Repositories,
	tx wagering.WagerTransaction,
	w wallet.Wallet,
	in ProcessWagerInput,
	now time.Time,
	result *ProcessWagerResult,
) error {
	return uc.applyCreditWithRef(ctx, repos, tx, w, in, nil, now, result)
}

func (uc *ProcessWagerTransaction) applyCreditWithRef(
	ctx context.Context,
	repos Repositories,
	tx wagering.WagerTransaction,
	w wallet.Wallet,
	in ProcessWagerInput,
	refID *uuid.UUID,
	now time.Time,
	result *ProcessWagerResult,
) error {
	entryID, err := newID()
	if err != nil {
		return err
	}
	expectedVersion := w.Version()
	mov, err := w.Credit(entryID, tx.ID(), tx.Amount(), now)
	if err != nil {
		return err
	}
	return uc.commitMovement(ctx, repos, tx, mov, expectedVersion, refID, in, now, result)
}

func (uc *ProcessWagerTransaction) applyLoss(
	ctx context.Context,
	repos Repositories,
	tx wagering.WagerTransaction,
	w wallet.Wallet,
	in ProcessWagerInput,
	now time.Time,
	result *ProcessWagerResult,
) error {
	processed, err := tx.MarkProcessed(w.Balance(), nil, now)
	if err != nil {
		return err
	}
	if err := repos.Transactions().Update(ctx, processed); err != nil {
		return err
	}
	evID, err := newID()
	if err != nil {
		return err
	}
	env, err := event.NewWagerTransactionProcessed(evID, processed, w.Balance(), in.CorrelationID, in.CausationID, now)
	if err != nil {
		return err
	}
	if err := repos.Outbox().Insert(ctx, env); err != nil {
		return err
	}
	result.Transaction = processed
	result.Balance = w.Balance()
	return nil
}

func (uc *ProcessWagerTransaction) commitMovement(
	ctx context.Context,
	repos Repositories,
	tx wagering.WagerTransaction,
	mov wallet.MovementResult,
	expectedVersion int64,
	refID *uuid.UUID,
	in ProcessWagerInput,
	now time.Time,
	result *ProcessWagerResult,
) error {
	processed, err := tx.MarkProcessed(mov.Wallet.Balance(), refID, now)
	if err != nil {
		return err
	}
	if err := repos.Wallets().Update(ctx, mov.Wallet, expectedVersion); err != nil {
		return err
	}
	if err := repos.Ledger().Insert(ctx, mov.Entry); err != nil {
		return err
	}
	if err := repos.Transactions().Update(ctx, processed); err != nil {
		return err
	}
	if err := repos.ReferenceRetries().Delete(ctx, processed.ID()); err != nil {
		return err
	}

	ev1, err := newID()
	if err != nil {
		return err
	}
	processedEv, err := event.NewWagerTransactionProcessed(ev1, processed, mov.Wallet.Balance(), in.CorrelationID, in.CausationID, now)
	if err != nil {
		return err
	}
	if err := repos.Outbox().Insert(ctx, processedEv); err != nil {
		return err
	}
	ev2, err := newID()
	if err != nil {
		return err
	}
	changedEv, err := event.NewWalletBalanceChanged(ev2, mov.Entry, mov.Wallet.Version(), in.CorrelationID, in.CausationID, now)
	if err != nil {
		return err
	}
	if err := repos.Outbox().Insert(ctx, changedEv); err != nil {
		return err
	}

	result.Transaction = processed
	result.Balance = mov.Wallet.Balance()
	return nil
}

func rejectAndPersist(
	ctx context.Context,
	repos Repositories,
	tx wagering.WagerTransaction,
	balance money.Money,
	code apperr.Code,
	in ProcessWagerInput,
	now time.Time,
	result *ProcessWagerResult,
) error {
	rejected, err := tx.MarkRejected(code, now)
	if err != nil {
		return err
	}
	minor := balance.AmountMinor()
	rejected = setResultBalance(rejected, minor)
	if err := repos.Transactions().Update(ctx, rejected); err != nil {
		return err
	}
	_ = repos.ReferenceRetries().Delete(ctx, rejected.ID())
	evID, err := newID()
	if err != nil {
		return err
	}
	env, err := event.NewWagerTransactionRejected(evID, rejected, in.CorrelationID, in.CausationID, now)
	if err != nil {
		return err
	}
	if err := repos.Outbox().Insert(ctx, env); err != nil {
		return err
	}
	result.Transaction = rejected
	result.Balance = balance
	return nil
}

func setResultBalance(tx wagering.WagerTransaction, minor int64) wagering.WagerTransaction {
	s := wagering.PersistState{
		ID:                             tx.ID(),
		Origin:                         tx.Origin(),
		ProviderID:                     tx.ProviderID(),
		ExternalTransactionID:          tx.ExternalTransactionID(),
		IdempotencyKey:                 tx.IdempotencyKey(),
		PayloadHash:                    tx.PayloadHash(),
		WalletID:                       tx.WalletID(),
		PlayerID:                       tx.PlayerID(),
		RoundID:                        tx.RoundID(),
		GameID:                         tx.GameID(),
		Kind:                           tx.Kind(),
		Amount:                         tx.Amount(),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		ReferenceTransactionID:         tx.ReferenceTransactionID(),
		Status:                         tx.Status(),
		FailureCode:                    tx.FailureCode(),
		ResultBalanceMinor:             &minor,
		CreatedAt:                      tx.CreatedAt(),
		UpdatedAt:                      tx.UpdatedAt(),
	}
	out, err := wagering.Rehydrate(s)
	if err != nil {
		return tx
	}
	return out
}

func validateReferenceMatch(tx, ref wagering.WagerTransaction) error {
	if ref.ProviderID() != tx.ProviderID() ||
		ref.PlayerID() != tx.PlayerID() ||
		ref.WalletID() != tx.WalletID() ||
		ref.RoundID() != tx.RoundID() ||
		ref.Amount().Currency() != tx.Amount().Currency() {
		return apperr.NewFailure(apperr.CodeReferenceMismatch, "reference fields do not match operation")
	}
	if !ref.Amount().Equal(tx.Amount()) {
		return apperr.NewFailure(apperr.CodeReferenceAmountMismatch, "reversal amount must equal referenced amount")
	}
	return nil
}

func snapshotBalance(tx wagering.WagerTransaction) (money.Money, error) {
	minor := tx.ResultBalanceMinor()
	if minor == nil {
		return money.Money{}, fmt.Errorf("no result balance snapshot")
	}
	return money.FromMinor(*minor, tx.Amount().Currency())
}

func newID() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New(), nil
	}
	return id, nil
}

func mapInsertConflict(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, apperr.ErrConflict) {
		return apperr.WrapFailure(apperr.CodeConflict, "conflict on wager transaction identity", err)
	}
	return err
}
