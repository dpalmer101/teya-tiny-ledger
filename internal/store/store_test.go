package store_test

import (
	"errors"
	"testing"
	"time"

	"ledger/internal/store"
)

// setup creates a fresh store and one account.
func setup(t *testing.T) (*store.MemoryStore, store.Account) {
	t.Helper()
	s := store.NewStore()
	acc, err := s.CreateAccount("GBP")
	if err != nil {
		t.Fatalf("setup: CreateAccount: %v", err)
	}
	return s, acc
}

// mustAddTx records a transaction and fails the test immediately on error.
func mustAddTx(t *testing.T, s *store.MemoryStore, accountID string, input store.NewTransaction) store.Transaction {
	t.Helper()
	tx, err := s.AddTransaction(accountID, input)
	if err != nil {
		t.Fatalf("AddTransaction: %v", err)
	}
	return tx
}

// date is a convenience for building a UTC time.Time in tests.
func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// ---- CreateAccount -------------------------------------------------------

func TestCreateAccount(t *testing.T) {
	s := store.NewStore()
	acc, err := s.CreateAccount("GBP")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acc.ID == "" {
		t.Error("ID is empty")
	}
	if acc.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
}

// ---- GetBalance ----------------------------------------------------------

func TestGetBalance(t *testing.T) {
	tests := []struct {
		name      string
		amounts   []int64
		noAccount bool
		wantBal   int64
		wantErr   error
	}{
		{name: "new account is zero", wantBal: 0},
		{name: "sums credits and debits", amounts: []int64{500, 300, -100}, wantBal: 700},
		{name: "balance can go negative", amounts: []int64{100, -200}, wantBal: -100},
		{name: "unknown account returns error", noAccount: true, wantErr: store.ErrAccountNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, acc := setup(t)
			for _, amount := range tt.amounts {
				if _, err := s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP", Amount: amount, TransactionDate: time.Now()}); err != nil {
					t.Fatalf("AddTransaction(%d): %v", amount, err)
				}
			}
			accountID := acc.ID
			if tt.noAccount {
				accountID = "nonexistent"
			}
			bal, err := s.GetBalance(accountID)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && bal != tt.wantBal {
				t.Errorf("balance = %d, want %d", bal, tt.wantBal)
			}
		})
	}
}

func TestListTransactions_EmptyAccountReturnsNonNilSlice(t *testing.T) {
	s, acc := setup(t)
	txs, err := s.ListTransactions(acc.ID, store.TransactionQuery{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txs == nil {
		t.Error("ListTransactions returned nil, want empty non-nil slice")
	}
}

func TestAddTransaction_BalanceOverflow(t *testing.T) {
	s := store.NewStore()
	acc, _ := s.CreateAccount("GBP")

	const big = 1_000_000_000_000_000
	target := int64(9_223_372_036_854_775_806)
	for target > 0 {
		n := min(target, big)
		if _, err := s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP", Amount: n, TransactionDate: time.Now()}); err != nil {
			t.Fatalf("setup deposit: %v", err)
		}
		target -= n
	}
	before := func() int {
		txs, _ := s.ListTransactions(acc.ID, store.TransactionQuery{})
		return len(txs)
	}
	beforeCount := before()

	_, err := s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP", Amount: 2, TransactionDate: time.Now()})
	if !errors.Is(err, store.ErrBalanceOverflow) {
		t.Errorf("err = %v, want ErrBalanceOverflow", err)
	}
	if after := before(); after != beforeCount {
		t.Errorf("transaction count changed from %d to %d after overflow", beforeCount, after)
	}
}

func TestAddTransaction_BalanceUnderflow(t *testing.T) {
	s := store.NewStore()
	acc, _ := s.CreateAccount("GBP")

	// Debit by (MinInt64 + 1), landing the balance at exactly MinInt64 + 1.
	// A subsequent debit of 2 would require MinInt64 - 1, which underflows.
	const nearMin = int64(-9_223_372_036_854_775_807) // MinInt64 + 1
	if _, err := s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP", Amount: nearMin, TransactionDate: time.Now()}); err != nil {
		t.Fatalf("setup debit: %v", err)
	}

	beforeCount := func() int {
		txs, _ := s.ListTransactions(acc.ID, store.TransactionQuery{})
		return len(txs)
	}()

	_, err := s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP", Amount: -2, TransactionDate: time.Now()})
	if !errors.Is(err, store.ErrBalanceOverflow) {
		t.Errorf("err = %v, want ErrBalanceOverflow for underflow", err)
	}
	if after := func() int {
		txs, _ := s.ListTransactions(acc.ID, store.TransactionQuery{})
		return len(txs)
	}(); after != beforeCount {
		t.Errorf("transaction count changed from %d to %d after underflow — ghost transaction inserted", beforeCount, after)
	}
}

// ---- AddTransaction validation -------------------------------------------

func TestAddTransaction_Validation(t *testing.T) {
	s, acc := setup(t)

	tests := []struct {
		name    string
		input   store.NewTransaction
		wantErr error
	}{
		{name: "deposit accepted", input: store.NewTransaction{Currency: "GBP", Amount: 100, TransactionDate: date(2024, 1, 1)}},
		{name: "negative amount (debit) accepted", input: store.NewTransaction{Currency: "GBP", Amount: -100, TransactionDate: date(2024, 1, 1)}},
		{name: "zero amount rejected", input: store.NewTransaction{Currency: "GBP", Amount: 0, TransactionDate: date(2024, 1, 1)}, wantErr: store.ErrInvalidAmount},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := s.AddTransaction(acc.ID, tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if tx.ID == "" {
				t.Error("ID is empty")
			}
			if tx.AccountID != acc.ID {
				t.Errorf("AccountID = %q, want %q", tx.AccountID, acc.ID)
			}
			if tx.Amount != tt.input.Amount {
				t.Errorf("Amount = %d, want %d", tx.Amount, tt.input.Amount)
			}
		})
	}
}

func TestAddTransaction_UnknownAccount(t *testing.T) {
	s := store.NewStore()
	_, err := s.AddTransaction("nonexistent", store.NewTransaction{Currency: "GBP", Amount: 100, TransactionDate: time.Now()})
	if !errors.Is(err, store.ErrAccountNotFound) {
		t.Errorf("err = %v, want ErrAccountNotFound", err)
	}
}

// ---- AddTransaction dates ------------------------------------------------

func TestAddTransaction_TransactionDate_IsPreserved(t *testing.T) {
	s, acc := setup(t)
	explicit := time.Date(2024, 1, 13, 9, 0, 0, 0, time.UTC)
	tx, err := s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP", Amount: 100, TransactionDate: explicit})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tx.TransactionDate.Equal(explicit) {
		t.Errorf("TransactionDate = %v, want %v", tx.TransactionDate, explicit)
	}
}

func TestAddTransaction_CreatedAtIsAlwaysServerTime(t *testing.T) {
	s, acc := setup(t)
	past := date(2020, 1, 1)
	before := time.Now()
	tx, err := s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP", Amount: 100, TransactionDate: past})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.CreatedAt.Before(before) {
		t.Errorf("CreatedAt %v should not be before server time %v", tx.CreatedAt, before)
	}
}

// ---- ListTransactions ordering -------------------------------------------

func TestListTransactions_NewestFirst(t *testing.T) {
	s, acc := setup(t)

	for _, ins := range []struct {
		d    time.Time
		desc string
	}{
		{date(2024, 1, 15), "third"},
		{date(2024, 1, 13), "first"},
		{date(2024, 1, 14), "second"},
	} {
		mustAddTx(t, s, acc.ID, store.NewTransaction{Currency: "GBP", Amount: 100, Description: ins.desc, TransactionDate: ins.d})
	}

	txs, err := s.ListTransactions(acc.ID, store.TransactionQuery{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, want := range []string{"third", "second", "first"} {
		if txs[i].Description != want {
			t.Errorf("txs[%d].Description = %q, want %q", i, txs[i].Description, want)
		}
	}
}

func TestListTransactions_SameDateReversesInsertionOrder(t *testing.T) {
	s, acc := setup(t)
	d := date(2024, 1, 14)
	for _, desc := range []string{"a", "b", "c"} {
		mustAddTx(t, s, acc.ID, store.NewTransaction{Currency: "GBP", Amount: 100, Description: desc, TransactionDate: d})
	}
	txs, _ := s.ListTransactions(acc.ID, store.TransactionQuery{})
	for i, want := range []string{"c", "b", "a"} {
		if txs[i].Description != want {
			t.Errorf("txs[%d].Description = %q, want %q", i, txs[i].Description, want)
		}
	}
}

// ---- ListTransactions pagination -----------------------------------------

func TestListTransactions_Pagination(t *testing.T) {
	s, acc := setup(t)
	base := date(2024, 1, 1)
	for i := range 5 {
		d := base.AddDate(0, 0, i)
		mustAddTx(t, s, acc.ID, store.NewTransaction{Currency: "GBP", Amount: int64(i+1) * 100, TransactionDate: d})
	}

	page1, err := s.ListTransactions(acc.ID, store.TransactionQuery{Limit: 2})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page 1 len = %d, want 2", len(page1))
	}

	page2, err := s.ListTransactions(acc.ID, store.TransactionQuery{AfterID: page1[1].ID, Limit: 2})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("page 2 len = %d, want 2", len(page2))
	}

	page3, err := s.ListTransactions(acc.ID, store.TransactionQuery{AfterID: page2[1].ID, Limit: 2})
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(page3) != 1 {
		t.Fatalf("page 3 len = %d, want 1", len(page3))
	}

	all := append(append(page1, page2...), page3...)
	seen := map[string]bool{}
	for _, tx := range all {
		if seen[tx.ID] {
			t.Errorf("duplicate transaction %s", tx.ID)
		}
		seen[tx.ID] = true
	}
	if len(seen) != 5 {
		t.Errorf("total unique transactions = %d, want 5", len(seen))
	}
}

func TestListTransactions_InvalidCursor(t *testing.T) {
	s, acc := setup(t)
	_, err := s.ListTransactions(acc.ID, store.TransactionQuery{AfterID: "nonexistent"})
	if !errors.Is(err, store.ErrCursorNotFound) {
		t.Errorf("err = %v, want ErrCursorNotFound", err)
	}
}

// ---- ListTransactions date filter ----------------------------------------

func TestListTransactions_DateFilter(t *testing.T) {
	s, acc := setup(t)

	for _, d := range []time.Time{date(2024, 1, 13), date(2024, 1, 14), date(2024, 1, 15)} {
		mustAddTx(t, s, acc.ID, store.NewTransaction{Currency: "GBP", Amount: 100, TransactionDate: d})
	}

	since := date(2024, 1, 14)
	until := date(2024, 1, 14)

	tests := []struct {
		name    string
		q       store.TransactionQuery
		wantLen int
	}{
		{"no filter returns all", store.TransactionQuery{}, 3},
		{"since filters lower bound", store.TransactionQuery{Since: &since}, 2},
		{"until filters upper bound", store.TransactionQuery{Until: &until}, 2},
		{"since and until combined", store.TransactionQuery{Since: &since, Until: &until}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			txs, err := s.ListTransactions(acc.ID, tt.q)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(txs) != tt.wantLen {
				t.Errorf("len = %d, want %d", len(txs), tt.wantLen)
			}
		})
	}
}

func TestListTransactions_CursorWithDateFilter(t *testing.T) {
	s, acc := setup(t)

	for _, d := range []time.Time{
		date(2024, 1, 11), date(2024, 1, 12), date(2024, 1, 13),
		date(2024, 1, 14), date(2024, 1, 15),
	} {
		mustAddTx(t, s, acc.ID, store.NewTransaction{Currency: "GBP", Amount: 100, TransactionDate: d})
	}

	page1, err := s.ListTransactions(acc.ID, store.TransactionQuery{Limit: 2})
	if err != nil || len(page1) != 2 {
		t.Fatalf("page1: err=%v len=%d", err, len(page1))
	}
	cursor := page1[len(page1)-1].ID

	since := date(2024, 1, 12)
	txsSince, err := s.ListTransactions(acc.ID, store.TransactionQuery{AfterID: cursor, Since: &since})
	if err != nil {
		t.Fatalf("since case: %v", err)
	}
	if len(txsSince) != 2 {
		t.Fatalf("since: len = %d, want 2", len(txsSince))
	}

	until := date(2024, 1, 12)
	txsUntil, err := s.ListTransactions(acc.ID, store.TransactionQuery{AfterID: cursor, Until: &until})
	if err != nil {
		t.Fatalf("until case: %v", err)
	}
	if len(txsUntil) != 2 {
		t.Fatalf("until: len = %d, want 2", len(txsUntil))
	}

	// Boundary: cursor's TransactionDate equals Until exactly.
	// The cursor is Jan 14 and Until is also Jan 14. The code must find the
	// cursor within its date group without returning ErrCursorNotFound, and
	// must return the three transactions older than the cursor that also satisfy
	// Until (Jan 13, Jan 12, Jan 11).
	cursorDate := date(2024, 1, 14)
	txsBoundary, err := s.ListTransactions(acc.ID, store.TransactionQuery{AfterID: cursor, Until: &cursorDate})
	if err != nil {
		t.Fatalf("boundary case (cursor date == Until): %v", err)
	}
	if len(txsBoundary) != 3 {
		t.Fatalf("boundary: len = %d, want 3 (Jan13, Jan12, Jan11 are older than cursor and within Until)", len(txsBoundary))
	}
}

func TestListTransactions_UnlimitedViaZeroLimit(t *testing.T) {
	s, acc := setup(t)
	base := date(2024, 1, 1)
	for i := range 5 {
		mustAddTx(t, s, acc.ID, store.NewTransaction{Currency: "GBP", Amount: 100, TransactionDate: base.AddDate(0, 0, i)})
	}
	txs, err := s.ListTransactions(acc.ID, store.TransactionQuery{Limit: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(txs) != 5 {
		t.Errorf("len = %d, want 5", len(txs))
	}
}

func TestListTransactions_UnknownAccount(t *testing.T) {
	s := store.NewStore()
	_, err := s.ListTransactions("nonexistent", store.TransactionQuery{})
	if !errors.Is(err, store.ErrAccountNotFound) {
		t.Errorf("err = %v, want ErrAccountNotFound", err)
	}
}

// ---- Cross-account isolation --------------------------------------------

func TestCrossAccountIsolation(t *testing.T) {
	s := store.NewStore()
	accA, _ := s.CreateAccount("GBP")
	accB, _ := s.CreateAccount("GBP")

	mustAddTx(t, s, accA.ID, store.NewTransaction{Currency: "GBP", Amount: 1000, TransactionDate: date(2024, 1, 1)})
	mustAddTx(t, s, accB.ID, store.NewTransaction{Currency: "GBP", Amount: 500, TransactionDate: date(2024, 1, 1)})
	mustAddTx(t, s, accA.ID, store.NewTransaction{Currency: "GBP", Amount: -200, TransactionDate: date(2024, 1, 2)})

	balA, _ := s.GetBalance(accA.ID)
	balB, _ := s.GetBalance(accB.ID)
	if balA != 800 {
		t.Errorf("account A balance = %d, want 800", balA)
	}
	if balB != 500 {
		t.Errorf("account B balance = %d, want 500", balB)
	}

	txsA, _ := s.ListTransactions(accA.ID, store.TransactionQuery{})
	txsB, _ := s.ListTransactions(accB.ID, store.TransactionQuery{})
	if len(txsA) != 2 {
		t.Errorf("account A transactions = %d, want 2", len(txsA))
	}
	if len(txsB) != 1 {
		t.Errorf("account B transactions = %d, want 1", len(txsB))
	}
}

// ---- Concurrent correctness ----------------------------------------------

func TestAddTransaction_ConcurrentBalance(t *testing.T) {
	s := store.NewStore()
	acc, _ := s.CreateAccount("GBP")

	const goroutines = 100
	done := make(chan struct{}, goroutines)
	for range goroutines {
		go func() {
			s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP", Amount: 1, TransactionDate: time.Now()}) //nolint:errcheck
			done <- struct{}{}
		}()
	}
	for range goroutines {
		<-done
	}

	bal, err := s.GetBalance(acc.ID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if bal != goroutines {
		t.Errorf("balance = %d, want %d", bal, goroutines)
	}
}

func TestListTransactions_ConcurrentReadWrite(t *testing.T) {
	s := store.NewStore()
	acc, _ := s.CreateAccount("GBP")
	const seed = 10
	for i := range seed {
		mustAddTx(t, s, acc.ID, store.NewTransaction{Currency: "GBP", Amount: 1, TransactionDate: time.Now().Add(time.Duration(i) * time.Second)})
	}

	const writers = 50
	writeErrs := make([]error, writers)
	done := make(chan struct{}, writers*2)

	for i := range writers {
		go func(i int) {
			_, writeErrs[i] = s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP", Amount: 1, TransactionDate: time.Now()})
			done <- struct{}{}
		}(i)
	}
	for range writers {
		go func() {
			s.GetBalance(acc.ID)                                         //nolint:errcheck
			s.ListTransactions(acc.ID, store.TransactionQuery{Limit: 5}) //nolint:errcheck
			done <- struct{}{}
		}()
	}
	for range writers * 2 {
		<-done
	}

	successful := seed
	for _, e := range writeErrs {
		if e == nil {
			successful++
		}
	}

	bal, _ := s.GetBalance(acc.ID)
	if bal != int64(successful) {
		t.Errorf("balance = %d, want %d", bal, successful)
	}
}

func TestAddTransaction_ConcurrentDifferentAccounts(t *testing.T) {
	s := store.NewStore()
	accA, err := s.CreateAccount("GBP")
	if err != nil {
		t.Fatalf("CreateAccount A: %v", err)
	}
	accB, err := s.CreateAccount("GBP")
	if err != nil {
		t.Fatalf("CreateAccount B: %v", err)
	}

	const n = 50
	done := make(chan struct{}, n*2)
	for range n {
		go func() {
			s.AddTransaction(accA.ID, store.NewTransaction{Currency: "GBP", Amount: 1, TransactionDate: time.Now()}) //nolint:errcheck
			done <- struct{}{}
		}()
		go func() {
			s.AddTransaction(accB.ID, store.NewTransaction{Currency: "GBP", Amount: -1, TransactionDate: time.Now()}) //nolint:errcheck
			done <- struct{}{}
		}()
	}
	for range n * 2 {
		<-done
	}

	balA, _ := s.GetBalance(accA.ID)
	balB, _ := s.GetBalance(accB.ID)
	if balA != n {
		t.Errorf("account A balance = %d, want %d", balA, n)
	}
	if balB != -n {
		t.Errorf("account B balance = %d, want %d", balB, -n)
	}
}
