package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ledger/internal/api"
	"ledger/internal/store"
)

// newServer creates a test HTTP server backed by a fresh in-memory store.
// It registers a cleanup to close the server when the test finishes.
func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := store.NewStore()
	h := api.NewHandler(s)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// post makes a POST request, sending body as JSON if non-nil.
func post(t *testing.T, srv *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	var bodyReader io.Reader = http.NoBody
	contentType := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(b)
		contentType = "application/json"
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("new request %s: %v", path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// postWithKey makes a POST request with a JSON body and an Idempotency-Key header.
func postWithKey(t *testing.T, srv *httptest.Server, path string, body any, idempotencyKey string) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("new request %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// get makes a GET request and returns the response.
func get(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// decode reads the response body into v and closes it.
func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// mustCreateAccount creates an account with currency GBP and returns its ID.
func mustCreateAccount(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp := post(t, srv, "/accounts", map[string]string{"currency": "GBP"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account: got %d, want 201", resp.StatusCode)
	}
	var acc store.Account
	decode(t, resp, &acc)
	return acc.ID
}

// mustAddTransaction records a transaction and returns it, failing the test on error.
// Injects "transaction_date" and "currency" defaults if not provided by the caller.
func mustAddTransaction(t *testing.T, srv *httptest.Server, accountID string, input map[string]any) store.Transaction {
	t.Helper()
	if _, ok := input["transaction_date"]; !ok {
		input["transaction_date"] = time.Now().UTC().Format(time.RFC3339)
	}
	if _, ok := input["currency"]; !ok {
		input["currency"] = "GBP"
	}
	resp := post(t, srv, "/accounts/"+accountID+"/transactions", input)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("add transaction: got %d, body: %s", resp.StatusCode, body)
	}
	var tx store.Transaction
	decode(t, resp, &tx)
	return tx
}

// ---- POST /accounts -------------------------------------------------------

func TestCreateAccount_Success(t *testing.T) {
	srv := newServer(t)
	resp := post(t, srv, "/accounts", map[string]string{"currency": "GBP"})

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var acc store.Account
	decode(t, resp, &acc)
	if acc.ID == "" {
		t.Error("ID is empty")
	}
	if acc.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
}

func TestCreateAccount_Currency(t *testing.T) {
	srv := newServer(t)

	tests := []struct {
		name       string
		currency   string
		wantStatus int
	}{
		{"valid GBP", "GBP", http.StatusCreated},
		{"valid USD", "USD", http.StatusCreated},
		{"lowercase rejected", "gbp", http.StatusBadRequest},
		{"too short rejected", "GB", http.StatusBadRequest},
		{"too long rejected", "GBPX", http.StatusBadRequest},
		{"empty rejected", "", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := post(t, srv, "/accounts", map[string]string{"currency": tt.currency})
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			resp.Body.Close()
		})
	}
}

func TestAddTransaction_CurrencyMismatch(t *testing.T) {
	srv := newServer(t)
	// Create a GBP account, then try to record a USD transaction.
	gbpAccount := mustCreateAccount(t, srv)

	resp := post(t, srv, "/accounts/"+gbpAccount+"/transactions",
		map[string]any{"currency": "USD", "amount": 100, "transaction_date": "2024-01-01T00:00:00Z"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 for currency mismatch", resp.StatusCode)
	}
	resp.Body.Close()

	// Balance must remain zero — the rejected transaction must not be stored.
	var bal struct{ Balance int64 `json:"balance"` }
	decode(t, get(t, srv, "/accounts/"+gbpAccount+"/balance"), &bal)
	if bal.Balance != 0 {
		t.Errorf("balance = %d, want 0 after rejected currency mismatch", bal.Balance)
	}
}

// ---- GET /accounts/{id}/balance ------------------------------------------

func TestGetBalance_NewAccount(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	resp := get(t, srv, "/accounts/"+id+"/balance")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		AccountID string `json:"account_id"`
		Balance   int64  `json:"balance"`
	}
	decode(t, resp, &body)
	if body.AccountID != id {
		t.Errorf("account_id = %q, want %q", body.AccountID, id)
	}
	if body.Balance != 0 {
		t.Errorf("balance = %d, want 0", body.Balance)
	}
}

func TestGetBalance_AfterTransactions(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "type": "deposit", "amount": 500})
	mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "type": "deposit", "amount": 300})
	mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "amount": -100})

	resp := get(t, srv, "/accounts/"+id+"/balance")
	var body struct{ Balance int64 `json:"balance"` }
	decode(t, resp, &body)
	if body.Balance != 700 {
		t.Errorf("balance = %d, want 700", body.Balance)
	}
}

func TestGetBalance_NegativeBalance(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "type": "deposit", "amount": 100})
	// Debit exceeds credit; negative balance is explicitly allowed.
	resp := post(t, srv, "/accounts/"+id+"/transactions",
		map[string]any{"currency": "GBP", "amount": -200, "transaction_date": "2024-06-01T10:00:00Z"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("overdraft withdrawal: status = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()

	var bal struct{ Balance int64 `json:"balance"` }
	decode(t, get(t, srv, "/accounts/"+id+"/balance"), &bal)
	if bal.Balance != -100 {
		t.Errorf("balance = %d, want -100", bal.Balance)
	}
}

func TestAddTransaction_BalanceOverflow_Returns422(t *testing.T) {
	// ErrBalanceOverflow must map to 422 Unprocessable Entity, not 500.
	// We seed the balance close to MaxInt64 via the store directly, then
	// trigger the overflow via the HTTP layer.
	s := store.NewStore()
	acc, _ := s.CreateAccount("GBP")

	// Fill balance to MaxInt64 - 1 directly via the store.
	const big = 1_000_000_000_000_000
	target := int64(9_223_372_036_854_775_806)
	for target > 0 {
		n := min(target, big)
		if _, err := s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP", Amount: n}); err != nil {
			t.Fatalf("setup deposit: %v", err)
		}
		target -= n
	}

	h := api.NewHandler(s)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp := post(t, srv, "/accounts/"+acc.ID+"/transactions",
		map[string]any{"currency": "GBP", "amount": 2, "transaction_date": "2024-06-01T10:00:00Z"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestGetBalance_NotFound(t *testing.T) {
	srv := newServer(t)
	resp := get(t, srv, "/accounts/00000000-0000-4000-8000-000000000000/balance")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// ---- POST /accounts/{id}/transactions ------------------------------------

func TestAddTransaction_Success(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	tests := []struct {
		name  string
		input map[string]any
	}{
		{"credit", map[string]any{"currency": "GBP", "amount": 100, "description": "salary", "transaction_date": "2024-06-01T10:00:00Z"}},
		{"debit", map[string]any{"currency": "GBP", "amount": -50, "transaction_date": "2024-06-01T11:00:00Z"}},
		{"with transaction_date", map[string]any{"currency": "GBP", 
			"type": "deposit", "amount": 200,
			"transaction_date": "2024-01-13T09:00:00Z",
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := post(t, srv, "/accounts/"+id+"/transactions", tt.input)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("status = %d, want 201", resp.StatusCode)
			}
			var tx store.Transaction
			decode(t, resp, &tx)
			if tx.ID == "" {
				t.Error("ID is empty")
			}
			if tx.AccountID != id {
				t.Errorf("account_id = %q, want %q", tx.AccountID, id)
			}
			if tx.TransactionDate.IsZero() {
				t.Error("transaction_date is zero")
			}
			if tx.CreatedAt.IsZero() {
				t.Error("created_at is zero")
			}
		})
	}
}

func TestAddTransaction_TransactionDateIsPreserved(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	want := time.Date(2024, 1, 13, 9, 0, 0, 0, time.UTC)
	tx := mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", 
		"type": "deposit", "amount": 100,
		"transaction_date": want.Format(time.RFC3339),
	})

	if !tx.TransactionDate.Equal(want) {
		t.Errorf("transaction_date = %v, want %v", tx.TransactionDate, want)
	}
	if tx.CreatedAt.Equal(want) {
		t.Error("created_at should differ from transaction_date")
	}
}

func TestAddTransaction_MissingBody(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/accounts/"+id+"/transactions", http.NoBody)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing body", resp.StatusCode)
	}
}

func TestAddTransaction_ContentType(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	tests := []struct {
		name        string
		contentType string
		wantStatus  int
	}{
		{"no content-type", "", http.StatusUnsupportedMediaType},
		{"wrong content-type", "text/plain", http.StatusUnsupportedMediaType},
		{"json with charset", "application/json; charset=utf-8", http.StatusCreated},
		{"exact match", "application/json", http.StatusCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"amount":100,"currency":"GBP","transaction_date":"2024-06-01T10:00:00Z"}`)
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/accounts/"+id+"/transactions", bytes.NewReader(body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestAddTransaction_OversizedInputs(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	t.Run("account ID over 64 bytes", func(t *testing.T) {
		longID := strings.Repeat("a", 65)
		resp := get(t, srv, "/accounts/"+longID+"/balance")
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
		resp.Body.Close()
	})

	t.Run("limit over 1000", func(t *testing.T) {
		resp := get(t, srv, "/accounts/"+id+"/transactions?limit=1001")
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
		resp.Body.Close()
	})

	t.Run("request body over 1 MB", func(t *testing.T) {
		// Build a body > maxRequestBytes (1 << 20) that passes earlier checks:
		// short description so the description-length check doesn't fire first,
		// integer amount so json.Number parsing succeeds at the start.
		// The key is that the JSON object itself is padded to exceed the limit.
		// We send JSON with a large redundant field that has no schema meaning.
		padding := strings.Repeat("x", (1<<20)+1)
		body := `{"amount":1,"currency":"GBP","transaction_date":"2024-06-01T10:00:00Z","_pad":"` + padding + `"}`
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/accounts/"+id+"/transactions",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("fractional amount", func(t *testing.T) {
		// json.Number parsing rejects fractional values; the stored amount must
		// not silently truncate 1.99 to 1.
		body := `{"amount":1.99,"currency":"GBP","transaction_date":"2024-06-01T10:00:00Z"}`
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/accounts/"+id+"/transactions",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("transaction_date far in past is accepted", func(t *testing.T) {
		// No bounds validation on transaction_date — any valid RFC 3339 value is accepted.
		resp := post(t, srv, "/accounts/"+id+"/transactions",
			map[string]any{"currency": "GBP", "type": "deposit", "amount": 1, "transaction_date": "1900-01-01T00:00:00Z"})
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("status = %d, want 201 for historic date", resp.StatusCode)
		}
		resp.Body.Close()
	})

	t.Run("transaction_date far in future is rejected", func(t *testing.T) {
		resp := post(t, srv, "/accounts/"+id+"/transactions",
			map[string]any{"currency": "GBP", "type": "deposit", "amount": 1, "transaction_date": "9999-01-01T00:00:00Z"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 for far-future date", resp.StatusCode)
		}
		resp.Body.Close()
	})
}

func TestAddTransaction_Errors(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	tests := []struct {
		name       string
		accountID  string
		input      map[string]any
		wantStatus int
	}{
		{
			name: "missing transaction_date", accountID: id,
			input:      map[string]any{"currency": "GBP", "amount": 100},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "zero amount", accountID: id,
			input:      map[string]any{"currency": "GBP", "amount": 0, "transaction_date": "2024-06-01T10:00:00Z"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "bad transaction_date", accountID: id,
			input:      map[string]any{"currency": "GBP", "amount": 100, "transaction_date": "not-a-date"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "unknown account", accountID: "00000000-0000-4000-8000-000000000000",
			input:      map[string]any{"currency": "GBP", "amount": 100, "transaction_date": "2024-06-01T10:00:00Z"},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := post(t, srv, "/accounts/"+tt.accountID+"/transactions", tt.input)
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			resp.Body.Close()
		})
	}
}

// ---- GET /accounts/{id}/transactions -------------------------------------

func TestListTransactions_Empty(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	resp := get(t, srv, "/accounts/"+id+"/transactions")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		AccountID    string               `json:"account_id"`
		Transactions []store.Transaction `json:"transactions"`
	}
	decode(t, resp, &body)
	if body.AccountID != id {
		t.Errorf("account_id = %q, want %q", body.AccountID, id)
	}
	// A nil slice marshals as JSON null; the handler must return [] not null.
	if body.Transactions == nil {
		t.Error("transactions is null, want empty array []")
	}
	if len(body.Transactions) != 0 {
		t.Errorf("transactions len = %d, want 0", len(body.Transactions))
	}
}

func TestListTransactions_NewestFirst(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	// Insert out of chronological order.
	for _, d := range []string{"2024-01-15T00:00:00Z", "2024-01-13T00:00:00Z", "2024-01-14T00:00:00Z"} {
		mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", 
			"type": "deposit", "amount": 100, "description": d, "transaction_date": d,
		})
	}

	resp := get(t, srv, "/accounts/"+id+"/transactions")
	var body struct {
		Transactions []store.Transaction `json:"transactions"`
	}
	decode(t, resp, &body)

	if len(body.Transactions) != 3 {
		t.Fatalf("len = %d, want 3", len(body.Transactions))
	}
	for i := 1; i < len(body.Transactions); i++ {
		prev := body.Transactions[i-1].TransactionDate
		curr := body.Transactions[i].TransactionDate
		if curr.After(prev) {
			t.Errorf("transactions[%d].transaction_date %v after transactions[%d].transaction_date %v — want newest-first", i, curr, i-1, prev)
		}
	}
}

func TestListTransactions_Pagination_NoFalseNextCursor(t *testing.T) {
	// When the number of transactions is exactly equal to the limit, there are
	// no more items and NextCursor must NOT be set. A previous implementation
	// used len(txs)==limit as the condition, which would emit a false cursor
	// here. The fix fetches limit+1 internally to distinguish "full page with
	// more" from "exactly limit items and nothing left".
	srv := newServer(t)
	id := mustCreateAccount(t, srv)
	for range 3 {
		mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "type": "deposit", "amount": 100})
	}

	var body struct {
		Transactions []store.Transaction `json:"transactions"`
		NextCursor   string               `json:"next_cursor"`
	}
	decode(t, get(t, srv, "/accounts/"+id+"/transactions?limit=3"), &body)
	if len(body.Transactions) != 3 {
		t.Fatalf("len = %d, want 3", len(body.Transactions))
	}
	if body.NextCursor != "" {
		t.Errorf("next_cursor = %q, want empty — page is exactly the full history", body.NextCursor)
	}
}

func TestListTransactions_Pagination(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)
	for range 5 {
		mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "type": "deposit", "amount": 100})
	}

	var body1 struct {
		Transactions []store.Transaction `json:"transactions"`
		NextCursor   string               `json:"next_cursor"`
	}
	decode(t, get(t, srv, "/accounts/"+id+"/transactions?limit=2"), &body1)
	if len(body1.Transactions) != 2 {
		t.Fatalf("page 1 len = %d, want 2", len(body1.Transactions))
	}
	if body1.NextCursor == "" {
		t.Fatal("page 1: next_cursor is empty, want a cursor")
	}

	var body2 struct {
		Transactions []store.Transaction `json:"transactions"`
		NextCursor   string               `json:"next_cursor"`
	}
	decode(t, get(t, srv, "/accounts/"+id+"/transactions?limit=2&after="+body1.NextCursor), &body2)
	if len(body2.Transactions) != 2 {
		t.Fatalf("page 2 len = %d, want 2", len(body2.Transactions))
	}

	var body3 struct {
		Transactions []store.Transaction `json:"transactions"`
		NextCursor   string               `json:"next_cursor"`
	}
	decode(t, get(t, srv, "/accounts/"+id+"/transactions?limit=2&after="+body2.NextCursor), &body3)
	if len(body3.Transactions) != 1 {
		t.Fatalf("page 3 len = %d, want 1", len(body3.Transactions))
	}
	if body3.NextCursor != "" {
		t.Errorf("page 3: next_cursor = %q, want empty", body3.NextCursor)
	}
}

func TestListTransactions_CursorWithDateFilter(t *testing.T) {
	// Cursor establishes the page boundary; Since/Until then filter within
	// the remaining items. Verifies the two mechanisms compose correctly.
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	for _, d := range []string{
		"2024-01-11T00:00:00Z",
		"2024-01-12T00:00:00Z",
		"2024-01-13T00:00:00Z",
		"2024-01-14T00:00:00Z",
		"2024-01-15T00:00:00Z",
	} {
		mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", 
			"type": "deposit", "amount": 100, "transaction_date": d,
		})
	}

	// First page (newest-first, limit=2): [Jan15, Jan14].
	var page1 struct {
		Transactions []store.Transaction `json:"transactions"`
		NextCursor   string               `json:"next_cursor"`
	}
	decode(t, get(t, srv, "/accounts/"+id+"/transactions?limit=2"), &page1)
	if len(page1.Transactions) != 2 || page1.NextCursor == "" {
		t.Fatalf("unexpected page1: len=%d cursor=%q", len(page1.Transactions), page1.NextCursor)
	}

	t.Run("cursor with since", func(t *testing.T) {
		// Items older than Jan14 AND >= Jan12 → [Jan13, Jan12].
		var body struct {
			Transactions []store.Transaction `json:"transactions"`
		}
		decode(t, get(t, srv, "/accounts/"+id+"/transactions?after="+page1.NextCursor+"&since=2024-01-12T00:00:00Z"), &body)
		if len(body.Transactions) != 2 {
			t.Fatalf("len = %d, want 2 (Jan13, Jan12)", len(body.Transactions))
		}
	})

	t.Run("cursor with until", func(t *testing.T) {
		// Items older than Jan14 AND <= Jan12 → [Jan12, Jan11].
		var body struct {
			Transactions []store.Transaction `json:"transactions"`
		}
		decode(t, get(t, srv, "/accounts/"+id+"/transactions?after="+page1.NextCursor+"&until=2024-01-12T00:00:00Z"), &body)
		if len(body.Transactions) != 2 {
			t.Fatalf("len = %d, want 2 (Jan12, Jan11)", len(body.Transactions))
		}
	})
}

func TestFutureDatedTransaction_IsRejected(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	resp := post(t, srv, "/accounts/"+id+"/transactions", map[string]any{"currency": "GBP", 
		"amount": 100, "transaction_date": "2099-01-01T00:00:00Z",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; future transaction_date should be rejected", resp.StatusCode)
	}
	resp.Body.Close()

	// Balance must remain zero — the rejected transaction must not be stored.
	var balBody struct{ Balance int64 `json:"balance"` }
	decode(t, get(t, srv, "/accounts/"+id+"/balance"), &balBody)
	if balBody.Balance != 0 {
		t.Errorf("balance = %d, want 0; rejected transaction must not affect balance", balBody.Balance)
	}
}

func TestListTransactions_SinceBeyondAllTransactions(t *testing.T) {
	// A since value in the future should return an empty list, not an error.
	srv := newServer(t)
	id := mustCreateAccount(t, srv)
	mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "type": "deposit", "amount": 100})

	var body struct {
		Transactions []store.Transaction `json:"transactions"`
	}
	decode(t, get(t, srv, "/accounts/"+id+"/transactions?since=2099-01-01T00:00:00Z"), &body)
	if len(body.Transactions) != 0 {
		t.Errorf("len = %d, want 0 for since in the future", len(body.Transactions))
	}
}

func TestListTransactions_InvertedSinceUntil(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	resp := get(t, srv, "/accounts/"+id+"/transactions?since=2024-01-15T00:00:00Z&until=2024-01-13T00:00:00Z")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for since > until", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestListTransactions_DateFilter(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	for _, d := range []string{"2024-01-13T00:00:00Z", "2024-01-14T00:00:00Z", "2024-01-15T00:00:00Z"} {
		mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", 
			"type": "deposit", "amount": 100, "transaction_date": d,
		})
	}

	count := func(url string) int {
		t.Helper()
		var body struct {
			Transactions []store.Transaction `json:"transactions"`
		}
		decode(t, get(t, srv, url), &body)
		return len(body.Transactions)
	}

	base := "/accounts/" + id + "/transactions"
	if n := count(base + "?since=2024-01-14T00:00:00Z"); n != 2 {
		t.Errorf("since filter: count = %d, want 2", n)
	}
	if n := count(base + "?until=2024-01-14T00:00:00Z"); n != 2 {
		t.Errorf("until filter: count = %d, want 2", n)
	}
	if n := count(base + "?since=2024-01-14T00:00:00Z&until=2024-01-14T00:00:00Z"); n != 1 {
		t.Errorf("since+until filter: count = %d, want 1", n)
	}
}

func TestListTransactions_Errors(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)
	mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "type": "deposit", "amount": 100})

	tests := []struct {
		name       string
		url        string
		wantStatus int
	}{
		{"unknown account", "/accounts/00000000-0000-4000-8000-000000000000/transactions", http.StatusNotFound},
		{"invalid limit", "/accounts/" + id + "/transactions?limit=0", http.StatusBadRequest},
		{"non-integer limit", "/accounts/" + id + "/transactions?limit=abc", http.StatusBadRequest},
		{"bad since", "/accounts/" + id + "/transactions?since=not-a-date", http.StatusBadRequest},
		{"bad until", "/accounts/" + id + "/transactions?until=not-a-date", http.StatusBadRequest},
		{"bad cursor", "/accounts/" + id + "/transactions?after=nonexistent", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := get(t, srv, tt.url)
			if resp.StatusCode != tt.wantStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("status = %d, want %d; body: %s", resp.StatusCode, tt.wantStatus, body)
			}
			resp.Body.Close()
		})
	}
}

// ---- Full end-to-end flow ------------------------------------------------

// errorStore is a store.Store implementation that returns a fixed error for
// all calls. Used to test writeStoreError covers all known sentinel values.
type errorStore struct{ err error }

func (s *errorStore) CreateAccount(string) (store.Account, error) { return store.Account{}, s.err }
func (s *errorStore) GetBalance(string) (int64, error)            { return 0, s.err }
func (s *errorStore) AddTransaction(string, store.NewTransaction) (store.Transaction, error) {
	return store.Transaction{}, s.err
}
func (s *errorStore) ListTransactions(string, store.TransactionQuery) ([]store.Transaction, error) {
	return nil, s.err
}
func (s *errorStore) BeginSession(string) error    { return s.err }
func (s *errorStore) CommitSession(string) error   { return s.err }
func (s *errorStore) RollbackSession(string) error { return s.err }

func TestWriteStoreError_AllSentinels(t *testing.T) {
	// Ensures every sentinel in store.go is explicitly mapped in writeStoreError.
	// If a new sentinel is added without a corresponding case, this test will
	// fail when the default branch returns 500 instead of the expected status.
	tests := []struct {
		err        error
		wantStatus int
	}{
		{store.ErrAccountNotFound, http.StatusNotFound},
		{store.ErrInvalidAmount, http.StatusBadRequest},
		{store.ErrCursorNotFound, http.StatusBadRequest},
		{store.ErrBalanceOverflow, http.StatusUnprocessableEntity},
		{store.ErrCurrencyMismatch, http.StatusUnprocessableEntity},
		{store.ErrSessionAlreadyOpen, http.StatusConflict},
		{store.ErrNoSessionOpen, http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			h := api.NewHandler(&errorStore{err: tt.err})
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			resp := get(t, srv, "/accounts/00000000-0000-4000-8000-000000000000/balance")
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d for sentinel %q", resp.StatusCode, tt.wantStatus, tt.err)
			}
			resp.Body.Close()
		})
	}
}

// ---- POST /accounts/{id}/begin|commit|rollback ----------------------------

func mustBeginSession(t *testing.T, srv *httptest.Server, accountID string) {
	t.Helper()
	resp := post(t, srv, "/accounts/"+accountID+"/begin", nil)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("begin session: got %d, body: %s", resp.StatusCode, body)
	}
	resp.Body.Close()
}

func mustCommitSession(t *testing.T, srv *httptest.Server, accountID string) {
	t.Helper()
	resp := post(t, srv, "/accounts/"+accountID+"/commit", nil)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("commit session: got %d, body: %s", resp.StatusCode, body)
	}
	resp.Body.Close()
}

func balance(t *testing.T, srv *httptest.Server, accountID string) int64 {
	t.Helper()
	var body struct{ Balance int64 `json:"balance"` }
	decode(t, get(t, srv, "/accounts/"+accountID+"/balance"), &body)
	return body.Balance
}

func txCount(t *testing.T, srv *httptest.Server, accountID string) int {
	t.Helper()
	var body struct {
		Transactions []store.Transaction `json:"transactions"`
	}
	decode(t, get(t, srv, "/accounts/"+accountID+"/transactions"), &body)
	return len(body.Transactions)
}

func TestBeginSession_Errors(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	tests := []struct {
		name       string
		accountID  string
		setup      func()
		wantStatus int
	}{
		{
			name:       "unknown account",
			accountID:  "00000000-0000-4000-8000-000000000000",
			wantStatus: http.StatusNotFound,
		},
		{
			name:      "invalid account id",
			accountID: "not-a-uuid",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:      "session already open",
			accountID: id,
			setup:     func() { mustBeginSession(t, srv, id) },
			wantStatus: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup()
			}
			resp := post(t, srv, "/accounts/"+tt.accountID+"/begin", nil)
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			resp.Body.Close()
		})
	}
}

func TestCommitSession_Errors(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	tests := []struct {
		name       string
		accountID  string
		wantStatus int
	}{
		{"unknown account", "00000000-0000-4000-8000-000000000000", http.StatusNotFound},
		{"invalid account id", "not-a-uuid", http.StatusBadRequest},
		{"no session open", id, http.StatusConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := post(t, srv, "/accounts/"+tt.accountID+"/commit", nil)
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			resp.Body.Close()
		})
	}
}

func TestRollbackSession_Errors(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	tests := []struct {
		name       string
		accountID  string
		wantStatus int
	}{
		{"unknown account", "00000000-0000-4000-8000-000000000000", http.StatusNotFound},
		{"invalid account id", "not-a-uuid", http.StatusBadRequest},
		{"no session open", id, http.StatusConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := post(t, srv, "/accounts/"+tt.accountID+"/rollback", nil)
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			resp.Body.Close()
		})
	}
}

func TestSession_BufferedTransactionsHiddenBeforeCommit(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)
	mustAddTransaction(t, srv, id, map[string]any{"amount": 500})

	mustBeginSession(t, srv, id)
	mustAddTransaction(t, srv, id, map[string]any{"amount": 200})
	mustAddTransaction(t, srv, id, map[string]any{"amount": -100})

	if bal := balance(t, srv, id); bal != 500 {
		t.Errorf("balance during session = %d, want 500", bal)
	}
	if n := txCount(t, srv, id); n != 1 {
		t.Errorf("transaction count during session = %d, want 1", n)
	}
}

func TestSession_CommitAppliesAll(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)
	mustAddTransaction(t, srv, id, map[string]any{"amount": 500})

	mustBeginSession(t, srv, id)
	mustAddTransaction(t, srv, id, map[string]any{"amount": 200})
	mustAddTransaction(t, srv, id, map[string]any{"amount": -100})
	mustCommitSession(t, srv, id)

	if bal := balance(t, srv, id); bal != 600 {
		t.Errorf("balance after commit = %d, want 600", bal)
	}
	if n := txCount(t, srv, id); n != 3 {
		t.Errorf("transaction count after commit = %d, want 3", n)
	}
}

func TestSession_RollbackDiscardsAll(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)
	mustAddTransaction(t, srv, id, map[string]any{"amount": 500})

	mustBeginSession(t, srv, id)
	mustAddTransaction(t, srv, id, map[string]any{"amount": 200})

	resp := post(t, srv, "/accounts/"+id+"/rollback", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rollback: got %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	if bal := balance(t, srv, id); bal != 500 {
		t.Errorf("balance after rollback = %d, want 500", bal)
	}
	if n := txCount(t, srv, id); n != 1 {
		t.Errorf("transaction count after rollback = %d, want 1", n)
	}
}

func TestSession_CommitOverflow(t *testing.T) {
	s := store.NewStore()
	acc, _ := s.CreateAccount("GBP")

	const big = 1_000_000_000_000_000
	target := int64(9_223_372_036_854_775_806)
	for target > 0 {
		n := min(target, big)
		s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP", Amount: n})
		target -= n
	}

	h := api.NewHandler(s)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mustBeginSession(t, srv, acc.ID)
	mustAddTransaction(t, srv, acc.ID, map[string]any{
		"currency": "GBP", "amount": 2, "transaction_date": "2024-06-01T10:00:00Z",
	})

	// Commit must fail with 422; session remains open so rollback must succeed.
	resp := post(t, srv, "/accounts/"+acc.ID+"/commit", nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("commit overflow: status = %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()

	resp = post(t, srv, "/accounts/"+acc.ID+"/rollback", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("rollback after failed commit: status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSession_NewSessionAfterCommit(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	mustBeginSession(t, srv, id)
	mustCommitSession(t, srv, id)

	resp := post(t, srv, "/accounts/"+id+"/begin", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("second begin after commit: status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSession_NewSessionAfterRollback(t *testing.T) {
	srv := newServer(t)
	id := mustCreateAccount(t, srv)

	mustBeginSession(t, srv, id)
	post(t, srv, "/accounts/"+id+"/rollback", nil).Body.Close()

	resp := post(t, srv, "/accounts/"+id+"/begin", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("second begin after rollback: status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSession_OtherAccountUnaffected(t *testing.T) {
	srv := newServer(t)
	idA := mustCreateAccount(t, srv)
	idB := mustCreateAccount(t, srv)

	mustBeginSession(t, srv, idA)
	mustAddTransaction(t, srv, idB, map[string]any{"amount": 100})

	if bal := balance(t, srv, idB); bal != 100 {
		t.Errorf("account B balance = %d, want 100; session on A must not affect B", bal)
	}
	if bal := balance(t, srv, idA); bal != 0 {
		t.Errorf("account A balance = %d, want 0 while session is open", bal)
	}
}

func TestListTransactions_LimitOne(t *testing.T) {
	// Tests the limit=1 boundary: the smallest page size that exercises the
	// limit+1 fetch, cursor emission, and second-page retrieval.
	srv := newServer(t)
	id := mustCreateAccount(t, srv)
	mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "type": "deposit", "amount": 100})
	mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "type": "deposit", "amount": 200})

	var p1 struct {
		Transactions []store.Transaction `json:"transactions"`
		NextCursor   string               `json:"next_cursor"`
	}
	decode(t, get(t, srv, "/accounts/"+id+"/transactions?limit=1"), &p1)
	if len(p1.Transactions) != 1 {
		t.Fatalf("page 1 len = %d, want 1", len(p1.Transactions))
	}
	if p1.NextCursor == "" {
		t.Fatal("page 1: NextCursor is empty, want a cursor")
	}

	var p2 struct {
		Transactions []store.Transaction `json:"transactions"`
		NextCursor   string               `json:"next_cursor"`
	}
	decode(t, get(t, srv, "/accounts/"+id+"/transactions?limit=1&after="+p1.NextCursor), &p2)
	if len(p2.Transactions) != 1 {
		t.Fatalf("page 2 len = %d, want 1", len(p2.Transactions))
	}
	if p2.NextCursor != "" {
		t.Errorf("page 2: NextCursor = %q, want empty (no more items)", p2.NextCursor)
	}
	if p1.Transactions[0].ID == p2.Transactions[0].ID {
		t.Error("pages return the same transaction")
	}
}

func TestListTransactions_UntilBeforeAllTransactions(t *testing.T) {
	// An until value that precedes all transactions should return an empty list.
	srv := newServer(t)
	id := mustCreateAccount(t, srv)
	mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", 
		"type": "deposit", "amount": 100,
		"transaction_date": "2024-06-01T00:00:00Z",
	})

	var body struct {
		Transactions []store.Transaction `json:"transactions"`
	}
	decode(t, get(t, srv, "/accounts/"+id+"/transactions?until=2024-01-01T00:00:00Z"), &body)
	if len(body.Transactions) != 0 {
		t.Errorf("len = %d, want 0 for until before all transactions", len(body.Transactions))
	}
}

func TestAddTransaction_Idempotency(t *testing.T) {
	const (
		txPath = "/transactions"
		date   = "2024-06-01T10:00:00Z"
	)
	body := map[string]any{"currency": "GBP", "amount": 100, "transaction_date": date}

	tests := []struct {
		name       string
		run        func(t *testing.T, srv *httptest.Server, accountPath string)
	}{
		{
			name: "same key returns same transaction ID",
			run: func(t *testing.T, srv *httptest.Server, accountPath string) {
				const key = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
				var tx1, tx2 store.Transaction
				decode(t, postWithKey(t, srv, accountPath+txPath, body, key), &tx1)
				decode(t, postWithKey(t, srv, accountPath+txPath, body, key), &tx2)
				if tx1.ID != tx2.ID {
					t.Errorf("same key: got different IDs %s vs %s", tx1.ID, tx2.ID)
				}
			},
		},
		{
			name: "same key does not create a duplicate transaction",
			run: func(t *testing.T, srv *httptest.Server, accountPath string) {
				const key = "550e8400-e29b-41d4-a716-446655440000"
				postWithKey(t, srv, accountPath+txPath, body, key).Body.Close()
				postWithKey(t, srv, accountPath+txPath, body, key).Body.Close()

				var listBody struct {
					Transactions []store.Transaction `json:"transactions"`
				}
				decode(t, get(t, srv, accountPath+txPath), &listBody)
				if len(listBody.Transactions) != 1 {
					t.Errorf("got %d transactions, want 1 — duplicate was stored", len(listBody.Transactions))
				}
			},
		},
		{
			name: "different keys create different transactions",
			run: func(t *testing.T, srv *httptest.Server, accountPath string) {
				var tx1, tx2 store.Transaction
				decode(t, postWithKey(t, srv, accountPath+txPath, body, "6ba7b810-9dad-11d1-80b4-00c04fd430c8"), &tx1)
				decode(t, postWithKey(t, srv, accountPath+txPath, map[string]any{"currency": "GBP", "amount": 200, "transaction_date": date}, "6ba7b811-9dad-11d1-80b4-00c04fd430c8"), &tx2)
				if tx1.ID == tx2.ID {
					t.Error("different keys returned same transaction ID")
				}
			},
		},
		{
			name: "non-UUID key returns 400",
			run: func(t *testing.T, srv *httptest.Server, accountPath string) {
				resp := postWithKey(t, srv, accountPath+txPath, body, "not-a-uuid")
				if resp.StatusCode != http.StatusBadRequest {
					t.Errorf("status = %d, want 400 for non-UUID Idempotency-Key", resp.StatusCode)
				}
				resp.Body.Close()
			},
		},
		{
			name: "no key processes normally without caching",
			run: func(t *testing.T, srv *httptest.Server, accountPath string) {
				resp1 := post(t, srv, accountPath+txPath, body)
				resp2 := post(t, srv, accountPath+txPath, body)
				var tx1, tx2 store.Transaction
				decode(t, resp1, &tx1)
				decode(t, resp2, &tx2)
				if tx1.ID == tx2.ID {
					t.Error("requests without a key returned the same transaction ID — should be unique")
				}
			},
		},
		{
			name: "errors are not cached — retry with same key can succeed",
			run: func(t *testing.T, srv *httptest.Server, accountPath string) {
				// First attempt: zero amount → 400 (error must not be cached)
				resp := postWithKey(t, srv, accountPath+txPath,
					map[string]any{"currency": "GBP", "amount": 0, "transaction_date": date}, "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11")
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("expected 400 for invalid amount, got %d", resp.StatusCode)
				}
				resp.Body.Close()

				// Second attempt: valid amount, same key → must succeed
				resp = postWithKey(t, srv, accountPath+txPath, body, "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11")
				if resp.StatusCode != http.StatusCreated {
					body, _ := io.ReadAll(resp.Body)
					t.Fatalf("expected 201 on retry, got %d: %s", resp.StatusCode, body)
				}
				resp.Body.Close()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServer(t)
			id := mustCreateAccount(t, srv)
			tt.run(t, srv, "/accounts/"+id)
		})
	}
}

func TestFullFlow(t *testing.T) {
	srv := newServer(t)

	// Create an account.
	id := mustCreateAccount(t, srv)

	// Balance starts at zero.
	resp := get(t, srv, "/accounts/"+id+"/balance")
	var bal struct{ Balance int64 `json:"balance"` }
	decode(t, resp, &bal)
	if bal.Balance != 0 {
		t.Errorf("initial balance = %d, want 0", bal.Balance)
	}

	// Record some transactions, including a backdated one.
	past := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "type": "deposit", "amount": 1000, "transaction_date": past})
	mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "type": "deposit", "amount": 500})
	mustAddTransaction(t, srv, id, map[string]any{"currency": "GBP", "amount": -200})

	// Balance reflects all three transactions.
	decode(t, get(t, srv, "/accounts/"+id+"/balance"), &bal)
	if bal.Balance != 1300 {
		t.Errorf("balance = %d, want 1300", bal.Balance)
	}

	// Transaction list is newest-first; backdated transaction should be last.
	var list struct {
		Transactions []store.Transaction `json:"transactions"`
	}
	decode(t, get(t, srv, "/accounts/"+id+"/transactions"), &list)
	if len(list.Transactions) != 3 {
		t.Fatalf("transaction count = %d, want 3", len(list.Transactions))
	}
	for i := 1; i < len(list.Transactions); i++ {
		if list.Transactions[i].TransactionDate.After(list.Transactions[i-1].TransactionDate) {
			t.Errorf("transactions not in newest-first order at index %d", i)
		}
	}
	last := list.Transactions[len(list.Transactions)-1]
	if !last.TransactionDate.Equal(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("last transaction_date = %v, want backdated 2024-01-01", last.TransactionDate)
	}
}
