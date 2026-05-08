package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"ledger/internal/store"
)

const (
	maxLimit = 1000 // server-side cap; prevents a single request returning unbounded history

	// Path and query parameter names.
	paramID    = "id"
	paramAfter = "after"
	paramLimit = "limit"
	paramSince = "since"
	paramUntil = "until"
)

type Handler struct {
	store       store.Store
	idempotency *Idempotency
}

func NewHandler(s store.Store) *Handler {
	return &Handler{
		store:       s,
		idempotency: &Idempotency{},
	}
}

type Mux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

func (h *Handler) RegisterRoutes(mux Mux) {
	mux.HandleFunc("POST /accounts", h.createAccount)
	mux.HandleFunc("GET /accounts/{id}/balance", h.getBalance)
	mux.HandleFunc("POST /accounts/{id}/transactions", h.idempotency.Wrap(h.addTransaction))
	mux.HandleFunc("GET /accounts/{id}/transactions", h.listTransactions)
}

// --- request / response types ---

type createAccountRequest struct {
	Currency string `json:"currency"`
}

type addTransactionRequest struct {
	Amount          json.Number `json:"amount"`
	Currency        string      `json:"currency"`
	TransactionDate string      `json:"transaction_date"`
	Description     string      `json:"description"`
}

type balanceResponse struct {
	AccountID string `json:"account_id"`
	Balance   int64  `json:"balance"`
}

type transactionListResponse struct {
	AccountID    string               `json:"account_id"`
	Transactions []store.Transaction `json:"transactions"`
	// NextCursor is the ID of the oldest transaction on the current page.
	NextCursor string `json:"next_cursor,omitempty"`
}

// --- handlers ---

func (h *Handler) createAccount(w http.ResponseWriter, r *http.Request) {
	var req createAccountRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if !validCurrency(req.Currency) {
		writeErrorString(w, http.StatusBadRequest, "currency must be a 3-letter ISO 4217 code (e.g. GBP, USD)")
		return
	}

	account, err := h.store.CreateAccount(req.Currency)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, account)
}

func (h *Handler) getBalance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue(paramID)
	if !validID(id) {
		writeErrorString(w, http.StatusBadRequest, "invalid account ID")
		return
	}

	balance, err := h.store.GetBalance(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, balanceResponse{
		AccountID: id,
		Balance:   balance,
	})
}

func (h *Handler) addTransaction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue(paramID)
	if !validID(id) {
		writeErrorString(w, http.StatusBadRequest, "invalid account ID")
		return
	}

	var req addTransactionRequest
	if !decodeBody(w, r, &req) {
		return
	}

	amount, err := req.Amount.Int64()
	if err != nil {
		writeErrorString(w, http.StatusBadRequest, "amount must be an integer")
		return
	}

	if req.TransactionDate == "" {
		writeErrorString(w, http.StatusBadRequest, "transaction_date is required")
		return
	}
	txDate, err := time.Parse(time.RFC3339, req.TransactionDate)
	if err != nil {
		writeErrorString(w, http.StatusBadRequest, "transaction_date must be RFC 3339 (e.g. 2024-01-15T10:00:00Z)")
		return
	}
	if txDate.After(time.Now()) {
		writeErrorString(w, http.StatusBadRequest, "transaction_date must not be in the future")
		return
	}
	if !validCurrency(req.Currency) {
		writeErrorString(w, http.StatusBadRequest, "currency must be a 3-letter ISO 4217 code (e.g. GBP, USD)")
		return
	}

	input := store.NewTransaction{
		Amount:          amount,
		Currency:        req.Currency,
		Description:     req.Description,
		TransactionDate: txDate,
	}

	tx, err := h.store.AddTransaction(id, input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, tx)
}

func (h *Handler) listTransactions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue(paramID)
	if !validID(id) {
		writeErrorString(w, http.StatusBadRequest, "invalid account ID")
		return
	}
	q := r.URL.Query()

	var query store.TransactionQuery

	query.AfterID = q.Get(paramAfter)
	if query.AfterID != "" && !validID(query.AfterID) {
		writeErrorString(w, http.StatusBadRequest, "invalid cursor")
		return
	}

	userLimit := 0
	if raw := q.Get(paramLimit); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeErrorString(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > maxLimit {
			writeErrorString(w, http.StatusBadRequest, "limit must not exceed 1000")
			return
		}
		userLimit = n
	}

	if raw := q.Get(paramSince); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeErrorString(w, http.StatusBadRequest, "since must be an RFC 3339 timestamp (e.g. 2006-01-02T15:04:05Z)")
			return
		}
		query.Since = &t
	}

	if raw := q.Get(paramUntil); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeErrorString(w, http.StatusBadRequest, "until must be an RFC 3339 timestamp (e.g. 2006-01-02T15:04:05Z)")
			return
		}
		query.Until = &t
	}

	if query.Since != nil && query.Until != nil && query.Since.After(*query.Until) {
		writeErrorString(w, http.StatusBadRequest, "since must not be after until")
		return
	}

	// Request one extra item to detect whether more transactions exist beyond
	// this page, without a separate count query.
	if userLimit > 0 {
		query.Limit = userLimit + 1
	}

	txs, err := h.store.ListTransactions(id, query)
	if err != nil {
		writeError(w, err)
		return
	}

	resp := transactionListResponse{AccountID: id}
	if userLimit > 0 && len(txs) > userLimit {
		// More items exist: truncate to the requested size and emit a cursor.
		txs = txs[:userLimit]
		resp.NextCursor = txs[len(txs)-1].ID
	}
	resp.Transactions = txs
	writeJSON(w, http.StatusOK, resp)
}

// --- helpers ---

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAccountNotFound):
		writeErrorString(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrInvalidAmount):
		writeErrorString(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrCursorNotFound):
		writeErrorString(w, http.StatusBadRequest, "invalid cursor")
	case errors.Is(err, store.ErrBalanceOverflow):
		writeErrorString(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, store.ErrCurrencyMismatch):
		writeErrorString(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeErrorString(w, http.StatusInternalServerError, "internal server error")
	}
}

func validID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

func validCurrency(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

