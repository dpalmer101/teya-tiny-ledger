package store

import (
	"errors"
	"time"
)

// Sentinel errors returned by Store implementations.
var (
	ErrAccountNotFound  = errors.New("account not found")
	ErrInvalidAmount    = errors.New("amount must not be zero")
	ErrCursorNotFound   = errors.New("cursor transaction not found")
	ErrBalanceOverflow  = errors.New("transaction would overflow the account balance")
	ErrCurrencyMismatch = errors.New("transaction currency does not match account currency")
	ErrSessionAlreadyOpen = errors.New("a transaction session is already open")
	ErrNoSessionOpen      = errors.New("no transaction session is open")
)

// Store is the interface that any ledger storage backend must satisfy.
type Store interface {
	CreateAccount(currency string) (Account, error)
	GetBalance(accountID string) (int64, error)
	AddTransaction(accountID string, input NewTransaction) (Transaction, error)
	ListTransactions(accountID string, q TransactionQuery) ([]Transaction, error)
	BeginSession(accountID string) error
	CommitSession(accountID string) error
	RollbackSession(accountID string) error
}

// NewTransaction is the input for recording a money movement.
// A positive Amount is a credit (deposit); a negative Amount is a debit
// (withdrawal). Zero is rejected. Currency must match the account's currency.
type NewTransaction struct {
	Amount          int64
	Currency        string
	Description     string
	TransactionDate time.Time // when the movement occurred; required, caller-supplied
}

// TransactionQuery controls which transactions ListTransactions returns.
// Zero values mean "no constraint": nil time pointers match any timestamp,
// empty AfterID starts from the beginning, Limit == 0 returns all results.
type TransactionQuery struct {
	AfterID string     // cursor: return transactions after this ID (exclusive)
	Limit   int        // max results; 0 (NoLimit) means "return all"
	Since   *time.Time // inclusive lower bound on TransactionDate
	Until   *time.Time // inclusive upper bound on TransactionDate
}

type Account struct {
	ID        string    `json:"id"`
	Currency  string    `json:"currency"`
	CreatedAt time.Time `json:"created_at"`
}

// Transaction records a single money movement. A positive Amount is a credit;
// a negative Amount is a debit.
type Transaction struct {
	ID              string    `json:"id"`
	AccountID       string    `json:"account_id"`
	Currency        string    `json:"currency"`
	Amount          int64     `json:"amount"`
	Description     string    `json:"description,omitempty"`
	TransactionDate time.Time `json:"transaction_date"`
	CreatedAt       time.Time `json:"created_at"`
}
