package store_test

import (
	"testing"
	"time"

	"ledger/internal/store"
)

const benchSize = 100_000

// populatedStore returns a store with a single account pre-loaded with n
// in-order transactions. Setup time is excluded from the benchmark timer.
func populatedStore(b *testing.B, n int) (*store.MemoryStore, string) {
	b.Helper()
	s := store.NewStore()
	acc, _ := s.CreateAccount("GBP")
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		d := base.Add(time.Duration(i) * time.Second)
		s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP",  //nolint:errcheck
			Amount: 100, TransactionDate: d,
		})
	}
	return s, acc.ID
}

// BenchmarkAddTransaction_InOrder measures appending transactions whose dates
// increase monotonically — the best case for sort-on-insert (copy 0 elements).
func BenchmarkAddTransaction_InOrder(b *testing.B) {
	s := store.NewStore()
	acc, _ := s.CreateAccount("GBP")
	base := time.Now()
	b.ResetTimer()
	for i := range b.N {
		d := base.Add(time.Duration(i) * time.Second)
		s.AddTransaction(acc.ID, store.NewTransaction{Currency: "GBP",  //nolint:errcheck
			Amount: 100, TransactionDate: d,
		})
	}
}

// BenchmarkAddTransaction_Backdated measures inserting at position 0 on a
// 100k-element slice — the worst case (copy all n elements on every insert).
// Each iteration grows the slice by one, so ns/op is an average of increasing
// costs rather than a stable steady-state figure. To get a stable worst-case
// number, use b.StopTimer()/b.StartTimer() to re-populate the store each
// iteration, at the cost of a slower benchmark run.
func BenchmarkAddTransaction_Backdated(b *testing.B) {
	s, accID := populatedStore(b, benchSize)
	// All subsequent inserts are dated before every existing transaction.
	before := time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)
	b.ResetTimer()
	for range b.N {
		s.AddTransaction(accID, store.NewTransaction{Currency: "GBP",  //nolint:errcheck
			Amount: 100, TransactionDate: before,
		})
	}
}

// BenchmarkAddTransaction_Parallel measures throughput under concurrent writes
// to the same account, exposing mutex contention. Each goroutine uses
// time.Now() so inserts land at the end of the sorted slice (in-order case),
// which is representative of the common production pattern.
func BenchmarkAddTransaction_Parallel(b *testing.B) {
	s := store.NewStore()
	acc, _ := s.CreateAccount("GBP")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.AddTransaction(acc.ID, store.NewTransaction{ //nolint:errcheck
				Currency:        "GBP",
				Amount:          100,
				TransactionDate: time.Now(),
			})
		}
	})
}

// BenchmarkGetBalance_100k measures the O(1) running-total lookup on a 100k-transaction account.
func BenchmarkGetBalance_100k(b *testing.B) {
	s, accID := populatedStore(b, benchSize)
	b.ResetTimer()
	for range b.N {
		s.GetBalance(accID) //nolint:errcheck
	}
}

// BenchmarkListTransactions_100k_Full measures a full unfiltered scan.
func BenchmarkListTransactions_100k_Full(b *testing.B) {
	s, accID := populatedStore(b, benchSize)
	b.ResetTimer()
	for range b.N {
		s.ListTransactions(accID, store.TransactionQuery{}) //nolint:errcheck
	}
}

// BenchmarkListTransactions_100k_DateFilter measures a scan that returns ~half
// the records via a Since bound.
func BenchmarkListTransactions_100k_DateFilter(b *testing.B) {
	s, accID := populatedStore(b, benchSize)
	// Roughly the midpoint of the dataset.
	mid := time.Date(2024, 1, 1, 1, 23, 20, 0, time.UTC)
	q := store.TransactionQuery{Since: &mid}
	b.ResetTimer()
	for range b.N {
		s.ListTransactions(accID, q) //nolint:errcheck
	}
}

// BenchmarkListTransactions_100k_FirstPage measures fetching the first page of
// 50 from a 100k-element list. No cursor to resolve — starts from the end of the slice.
func BenchmarkListTransactions_100k_FirstPage(b *testing.B) {
	s, accID := populatedStore(b, benchSize)
	q := store.TransactionQuery{Limit: 50}
	b.ResetTimer()
	for range b.N {
		s.ListTransactions(accID, q) //nolint:errcheck
	}
}

// BenchmarkListTransactions_100k_LastPage measures fetching the last 50 items
// from a 100k-element list using a cursor. Cursor lookup is O(1) via txIndex;
// the iteration cost is O(page size) = O(50), but the cursor sits near index 0
// of the ascending slice, making this the worst case for cursor resolution in
// a sorted-insert scan (all txIndex positions were previously shifted on each
// backdated insert). In practice this remains the most expensive read pattern.
func BenchmarkListTransactions_100k_LastPage(b *testing.B) {
	s, accID := populatedStore(b, benchSize)

	// Locate the cursor: the ID of the transaction immediately before the last page.
	all, _ := s.ListTransactions(accID, store.TransactionQuery{})
	cursorID := all[len(all)-51].ID

	q := store.TransactionQuery{AfterID: cursorID, Limit: 50}
	b.ResetTimer()
	for range b.N {
		s.ListTransactions(accID, q) //nolint:errcheck
	}
}
