package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/auth"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/observability"
)

type Dependencies struct {
	Health       *observability.HealthHandler
	Auth         *auth.Verifier
	Metrics      *observability.Metrics
	Log          *slog.Logger
	OpenWallet   *app.OpenWallet
	ProcessWager *app.ProcessWagerTransaction
	GetWallet    *app.GetWallet
	GetTx        *app.GetTransaction
	ListLedger   *app.ListLedger
	Reconcile    *app.ReconcileWallet
}

func NewMux(d Dependencies) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", d.Health.Live)
	mux.HandleFunc("GET /health/ready", d.Health.Ready)
	mux.Handle("GET /metrics", observability.MetricsHandler())

	authMW := d.Auth.Middleware
	internal := func(h http.HandlerFunc) http.Handler {
		return authMW(auth.RequireInternal(h))
	}
	write := func(h http.HandlerFunc) http.Handler {
		return authMW(auth.RequireRoles(auth.RoleWageringWrite)(h))
	}
	read := func(h http.HandlerFunc) http.Handler {
		return authMW(auth.RequireRoles(auth.RoleWageringRead, auth.RoleWalletAdmin)(h))
	}
	adminOrRead := func(h http.HandlerFunc) http.Handler {
		return authMW(auth.RequireRoles(auth.RoleWageringRead, auth.RoleWalletAdmin)(h))
	}

	mux.Handle("POST /wallets", internal(handleOpenWallet(d.OpenWallet)))
	mux.Handle("POST /wallets/{walletId}/reconciliation", internal(handleReconcile(d.Reconcile, d.Metrics, d.Log)))
	mux.Handle("GET /wallets/{walletId}", adminOrRead(handleGetWallet(d.GetWallet)))
	mux.Handle("GET /wallets/{walletId}/ledger", adminOrRead(handleListLedger(d.ListLedger)))
	mux.Handle("POST /wagering/transactions", write(handleProcessWager(d.ProcessWager, d.Metrics)))
	mux.Handle("GET /wagering/transactions/{transactionId}", read(handleGetTxByID(d.GetTx)))
	mux.Handle("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", read(handleGetTxByExternal(d.GetTx)))

	return correlationMiddleware(mux)
}

func correlationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cid := r.Header.Get("X-Correlation-Id")
		if cid == "" {
			cid = uuid.NewString()
		}
		w.Header().Set("X-Correlation-Id", cid)
		r.Header.Set("X-Correlation-Id", cid)
		ctx := observability.WithCorrelationID(r.Context(), cid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type openWalletRequest struct {
	PlayerID       string `json:"playerId"`
	InitialBalance struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"initialBalance"`
}

func handleOpenWallet(uc *app.OpenWallet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req openWalletRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid JSON", "")
			return
		}
		playerID, err := uuid.Parse(req.PlayerID)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid playerId", "")
			return
		}
		bal, err := money.Parse(req.InitialBalance.Amount, req.InitialBalance.Currency)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, err.Error(), "")
			return
		}
		out, err := uc.Execute(r.Context(), app.OpenWalletInput{
			PlayerID: playerID, InitialBalance: bal, CorrelationID: r.Header.Get("X-Correlation-Id"),
		})
		if err != nil {
			mapDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"id": out.Wallet.ID().String(), "playerId": out.Wallet.PlayerID().String(),
			"balance": moneyBody(out.Wallet.Balance()), "version": out.Wallet.Version(),
		})
	}
}

type wagerRequest struct {
	ProviderID            string `json:"providerId"`
	ExternalTransactionID string `json:"externalTransactionId"`
	PlayerID              string `json:"playerId"`
	WalletID              string `json:"walletId"`
	RoundID               string `json:"roundId"`
	GameID                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Money                 struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"money"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId"`
}

func handleProcessWager(uc *app.ProcessWagerTransaction, metrics *observability.Metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		p, ok := auth.FromContext(r.Context())
		if !ok {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "")
			return
		}
		idem := r.Header.Get("Idempotency-Key")
		if idem == "" {
			writeProblem(w, http.StatusBadRequest, "Idempotency-Key header is required", "")
			return
		}
		var req wagerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid JSON", "")
			return
		}
		if err := auth.RequireProviderMatch(req.ProviderID, p); err != nil {
			writeProblem(w, http.StatusForbidden, "providerId does not match token", "")
			return
		}
		playerID, err := uuid.Parse(req.PlayerID)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid playerId", "")
			return
		}
		walletID, err := uuid.Parse(req.WalletID)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid walletId", "")
			return
		}
		amt, err := money.Parse(req.Money.Amount, req.Money.Currency)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, err.Error(), "")
			return
		}
		ctx := r.Context()
		ctx = observability.WithProviderID(ctx, req.ProviderID)
		ctx = observability.WithWalletID(ctx, walletID.String())
		out, err := uc.Execute(ctx, app.ProcessWagerInput{
			ProviderID: req.ProviderID, ExternalTransactionID: req.ExternalTransactionID,
			IdempotencyKey: idem, PlayerID: playerID, WalletID: walletID,
			RoundID: req.RoundID, GameID: req.GameID, Kind: wagering.Kind(req.Kind), Money: amt,
			ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
			CorrelationID:                  observability.CorrelationID(ctx),
		})
		if metrics != nil {
			metrics.ObserveProcessing("http", req.Kind, time.Since(start))
		}
		if err != nil {
			if f, ok := apperr.AsFailure(err); ok && (errors.Is(err, apperr.ErrConflict) || f.Code == apperr.CodeConflict) {
				if metrics != nil {
					metrics.ConcurrencyConflicts.Inc()
				}
			}
			mapDomainError(w, err)
			return
		}
		if metrics != nil {
			metrics.TransactionsTotal.WithLabelValues(string(out.Transaction.Status()), req.Kind, "http").Inc()
			if out.IdempotentReplay {
				metrics.IdempotentReplays.Inc()
			}
		}
		writeWagerResult(w, out)
	}
}

func writeWagerResult(w http.ResponseWriter, out app.ProcessWagerResult) {
	body := map[string]any{
		"transactionId":    out.Transaction.ID().String(),
		"status":           string(out.Transaction.Status()),
		"idempotentReplay": out.IdempotentReplay,
	}
	if out.Transaction.Status() == wagering.StatusProcessed || out.Transaction.ResultBalanceMinor() != nil {
		body["balance"] = moneyBody(out.Balance)
	}
	if out.Transaction.FailureCode() != "" {
		body["failureCode"] = string(out.Transaction.FailureCode())
	}
	status := http.StatusOK
	switch out.Transaction.Status() {
	case wagering.StatusPendingReference:
		status = http.StatusAccepted
	case wagering.StatusRejected, wagering.StatusFailed:
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, body)
}

func handleGetWallet(uc *app.GetWallet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("walletId"))
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid walletId", "")
			return
		}
		wal, err := uc.Execute(r.Context(), id)
		if err != nil {
			mapDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": wal.ID().String(), "playerId": wal.PlayerID().String(),
			"balance": moneyBody(wal.Balance()), "version": wal.Version(),
		})
	}
}

func handleListLedger(uc *app.ListLedger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("walletId"))
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid walletId", "")
			return
		}
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		out, err := uc.Execute(r.Context(), app.ListLedgerInput{
			WalletID: id, Cursor: r.URL.Query().Get("cursor"), Limit: limit,
		})
		if err != nil {
			mapDomainError(w, err)
			return
		}
		items := make([]map[string]any, 0, len(out.Entries))
		for _, e := range out.Entries {
			items = append(items, map[string]any{
				"id": e.ID().String(), "transactionId": e.TransactionID().String(),
				"direction": string(e.Direction()), "money": moneyBody(e.Amount()),
				"balanceBefore": moneyBody(e.BalanceBefore()), "balanceAfter": moneyBody(e.BalanceAfter()),
				"createdAt": e.CreatedAt().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
			})
		}
		resp := map[string]any{"items": items}
		if out.NextCursor != "" {
			resp["nextCursor"] = out.NextCursor
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleGetTxByID(uc *app.GetTransaction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.FromContext(r.Context())
		id, err := uuid.Parse(r.PathValue("transactionId"))
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid transactionId", "")
			return
		}
		tx, err := uc.ByID(r.Context(), id)
		if err != nil {
			mapDomainError(w, err)
			return
		}
		if tx.Origin() == wagering.OriginExternal {
			if err := auth.RequireProviderMatch(tx.ProviderID(), p); err != nil {
				writeProblem(w, http.StatusNotFound, "transaction not found", "")
				return
			}
		} else if !p.IsInternal() {
			writeProblem(w, http.StatusNotFound, "transaction not found", "")
			return
		}
		writeJSON(w, http.StatusOK, txBody(tx))
	}
}

func handleGetTxByExternal(uc *app.GetTransaction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.FromContext(r.Context())
		providerID := r.PathValue("providerId")
		if err := auth.RequireProviderMatch(providerID, p); err != nil {
			writeProblem(w, http.StatusForbidden, "providerId does not match token", "")
			return
		}
		tx, err := uc.ByExternal(r.Context(), providerID, r.PathValue("externalTransactionId"))
		if err != nil {
			mapDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, txBody(tx))
	}
}

func handleReconcile(uc *app.ReconcileWallet, metrics *observability.Metrics, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("walletId"))
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid walletId", "")
			return
		}
		ctx := observability.WithWalletID(r.Context(), id.String())
		out, err := uc.Execute(ctx, id)
		if err != nil {
			mapDomainError(w, err)
			return
		}
		l := observability.LoggerFromContext(log, ctx)
		consistentLabel := "true"
		if !out.Consistent {
			consistentLabel = "false"
			if metrics != nil {
				metrics.ReconciliationDivergences.Inc()
			}
			l.Error("reconciliation divergence",
				"storedMinor", out.StoredBalance.AmountMinor(),
				"calculatedMinor", out.CalculatedBalance.AmountMinor(),
				"differenceMinor", out.Difference.AmountMinor(),
				"checkedEntries", out.CheckedEntries,
			)
		} else {
			l.Info("reconciliation consistent", "checkedEntries", out.CheckedEntries)
		}
		if metrics != nil {
			metrics.ReconciliationChecksTotal.WithLabelValues(consistentLabel).Inc()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"walletId":          out.WalletID.String(),
			"storedBalance":     moneyBody(out.StoredBalance),
			"calculatedBalance": moneyBody(out.CalculatedBalance),
			"difference":        moneyBody(out.Difference),
			"consistent":        out.Consistent,
			"checkedEntries":    out.CheckedEntries,
		})
	}
}

func txBody(tx wagering.WagerTransaction) map[string]any {
	body := map[string]any{
		"transactionId": tx.ID().String(),
		"status":        string(tx.Status()),
		"kind":          string(tx.Kind()),
		"walletId":      tx.WalletID().String(),
		"playerId":      tx.PlayerID().String(),
		"money":         moneyBody(tx.Amount()),
	}
	if tx.ProviderID() != "" {
		body["providerId"] = tx.ProviderID()
		body["externalTransactionId"] = tx.ExternalTransactionID()
	}
	if tx.FailureCode() != "" {
		body["failureCode"] = string(tx.FailureCode())
	}
	if tx.ResultBalanceMinor() != nil {
		if bal, err := money.FromMinor(*tx.ResultBalanceMinor(), tx.Amount().Currency()); err == nil {
			body["balance"] = moneyBody(bal)
		}
	}
	return body
}

func moneyBody(m money.Money) map[string]string {
	return map[string]string{"amount": m.String(), "currency": m.Currency()}
}

func mapDomainError(w http.ResponseWriter, err error) {
	if errors.Is(err, apperr.ErrTransient) {
		w.Header().Set("Retry-After", "2")
		detail := "dependency unavailable"
		if f, ok := apperr.AsFailure(err); ok && f.Message != "" {
			detail = f.Message
		}
		writeProblem(w, http.StatusServiceUnavailable, detail, "")
		return
	}
	if f, ok := apperr.AsFailure(err); ok {
		switch {
		case errors.Is(err, apperr.ErrUnauthorized):
			writeProblem(w, http.StatusUnauthorized, f.Message, string(f.Code))
		case errors.Is(err, apperr.ErrForbidden):
			writeProblem(w, http.StatusForbidden, f.Message, string(f.Code))
		case errors.Is(err, apperr.ErrNotFound) || f.Code == apperr.CodeWalletNotFound:
			writeProblem(w, http.StatusNotFound, f.Message, string(f.Code))
		case errors.Is(err, apperr.ErrConflict) || f.Code == apperr.CodeConflict:
			writeProblem(w, http.StatusConflict, f.Message, string(f.Code))
		case f.Code == apperr.CodeInvalidInput || f.Code == apperr.CodeInvalidAmount || f.Code == apperr.CodeKindNotAllowed:
			writeProblem(w, http.StatusBadRequest, f.Message, string(f.Code))
		default:
			writeProblem(w, http.StatusUnprocessableEntity, f.Message, string(f.Code))
		}
		return
	}
	if strings.Contains(strings.ToLower(err.Error()), "connect") {
		w.Header().Set("Retry-After", "2")
		writeProblem(w, http.StatusServiceUnavailable, "dependency unavailable", "")
		return
	}
	writeProblem(w, http.StatusInternalServerError, "internal error", "")
}

func writeProblem(w http.ResponseWriter, status int, detail, failureCode string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	body := map[string]any{
		"type":   "about:blank",
		"title":  http.StatusText(status),
		"status": status,
		"detail": detail,
	}
	if failureCode != "" {
		body["failureCode"] = failureCode
	}
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
