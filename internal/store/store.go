package store

import (
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Compile-time proof that *MemoryStore satisfies the Store interface.
var _ Store = (*MemoryStore)(nil)

// accountData holds all mutable state for a single account behind its own
// RWMutex. Reads on different accounts proceed concurrently; only writes (or
// reads concurrent with a write) on the same account serialise.
type accountData struct {
	mu           sync.RWMutex
	account      Account
	transactions []*Transaction
	balance      int64
	txLookup     map[string]*Transaction
}

// MemoryStore is an in-memory implementation of Store.
type MemoryStore struct {
	mu       sync.RWMutex
	accounts map[string]*accountData
}

func NewStore() *MemoryStore {
	return &MemoryStore{
		accounts: make(map[string]*accountData),
	}
}

func (s *MemoryStore) CreateAccount(currency string) (Account, error) {
	a := Account{
		ID:        uuid.New().String(),
		Currency:  currency,
		CreatedAt: time.Now().UTC(),
	}
	data := &accountData{
		account:      a,
		transactions: make([]*Transaction, 0),
		txLookup:     make(map[string]*Transaction),
	}

	s.mu.Lock()
	s.accounts[a.ID] = data
	s.mu.Unlock()

	return a, nil
}

func (s *MemoryStore) GetBalance(accountID string) (int64, error) {
	s.mu.RLock()
	data, ok := s.accounts[accountID]
	s.mu.RUnlock()
	if !ok {
		return 0, ErrAccountNotFound
	}

	data.mu.RLock()
	balance := data.balance
	data.mu.RUnlock()
	return balance, nil
}

func (s *MemoryStore) AddTransaction(accountID string, input NewTransaction) (Transaction, error) {
	if input.Amount == 0 {
		return Transaction{}, ErrInvalidAmount
	}

	// Generate the ID before acquiring any lock.
	id := uuid.New().String()

	s.mu.RLock()
	data, ok := s.accounts[accountID]
	s.mu.RUnlock()
	if !ok {
		return Transaction{}, ErrAccountNotFound
	}

	now := time.Now().UTC()
	txDate := input.TransactionDate.UTC()

	data.mu.Lock()
	defer data.mu.Unlock()

	if input.Currency != data.account.Currency {
		return Transaction{}, ErrCurrencyMismatch
	}

	// Guard against overflow/underflow.
	bal := data.balance
	newBalance := bal + input.Amount
	if (input.Amount > 0 && newBalance < bal) || (input.Amount < 0 && newBalance > bal) {
		return Transaction{}, ErrBalanceOverflow
	}

	tx := &Transaction{
		ID:              id,
		AccountID:       accountID,
		Currency:        input.Currency,
		Amount:          input.Amount,
		Description:     input.Description,
		TransactionDate: txDate,
		CreatedAt:       now,
	}

	// Insert in TransactionDate order. Binary search finds the insertion point
	// in O(log n); the element shift is O(n) but unavoidable for a slice store.
	txs := data.transactions
	i := sort.Search(len(txs), func(i int) bool {
		return txs[i].TransactionDate.After(txDate)
	})
	txs = append(txs, nil)
	copy(txs[i+1:], txs[i:])
	txs[i] = tx
	data.transactions = txs
	data.txLookup[tx.ID] = tx
	data.balance = newBalance

	return *tx, nil
}

func (s *MemoryStore) ListTransactions(accountID string, q TransactionQuery) ([]Transaction, error) {
	s.mu.RLock()
	data, ok := s.accounts[accountID]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrAccountNotFound
	}

	data.mu.RLock()
	defer data.mu.RUnlock()

	src := data.transactions

	// Iterate newest-first from the end of the ascending slice.
	start := len(src) - 1

	// Find the start transaction based on the earlier of the "cursor" transaction
	// and the "Until" date.
	var cursorTx *Transaction
	if q.AfterID != "" {
		var ok bool
		cursorTx, ok = data.txLookup[q.AfterID]
		if !ok {
			return nil, ErrCursorNotFound
		}
	}

	// Resolve the start position from cursor and/or Until. Three cases:
	//
	//   1. Cursor only — binary-search to the cursor's date, then scan the
	//      same-date group to find the exact transaction. start = cursor pos - 1.
	//
	//   2. Until only — binary-search to Until. start = last index <= Until.
	//
	//   3. Both — use whichever is earlier (tighter). If Until is earlier than
	//      the cursor's date, Until wins and the cursor is already excluded;
	//      start = hi-1. Otherwise the cursor's date wins, so scan for the
	//      cursor within its date group; start = cursor pos - 1.
	if cursorTx != nil || q.Until != nil {
		effectiveDate := time.Time{}
		if cursorTx != nil {
			effectiveDate = cursorTx.TransactionDate
		}
		if q.Until != nil && (effectiveDate.IsZero() || q.Until.Before(effectiveDate)) {
			effectiveDate = *q.Until
		}

		hi := sort.Search(len(src), func(i int) bool {
			return src[i].TransactionDate.After(effectiveDate)
		})

		if cursorTx != nil && (q.Until == nil || !cursorTx.TransactionDate.After(*q.Until)) {
			// Case 1 / Case 3 with cursor tighter or equal to Until.
			// Scan the same-date group leftward to locate the exact cursor.
			found := false
			for i := hi - 1; i >= 0 && !src[i].TransactionDate.Before(cursorTx.TransactionDate); i-- {
				if src[i].ID == q.AfterID {
					start = i - 1
					found = true
					break
				}
			}
			if !found {
				return nil, ErrCursorNotFound
			}
		} else {
			// Case 2 / Case 3 with Until tighter than cursor.
			start = hi - 1
		}
	}

	capacity := start + 1
	if q.Limit > 0 && q.Limit < capacity {
		capacity = q.Limit
	}
	out := make([]Transaction, 0, capacity)
	for i := start; i >= 0; i-- {
		tx := src[i]
		if q.Since != nil && tx.TransactionDate.Before(*q.Since) {
			break
		}
		out = append(out, *tx)
		if q.Limit > 0 && len(out) == q.Limit {
			break
		}
	}
	return out, nil
}
