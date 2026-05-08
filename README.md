# Ledger

A minimal REST API for recording and querying money movements. Full API reference in [api_docs.md](api_docs.md).

## Prerequisites

- Go 1.22 or later (`go version` to check)

## Testing

### All tests

Runs all unit and integration tests, starting a local HTTP server to execute integration tests against.

```bash
go test ./...
```

### Benchmark tests

Measures the performance-critical operations in the storage layer: in-order append, worst-case backdated insert (position 0 on a 100k-element slice), parallel write contention, balance lookup, and full / filtered / paginated list queries.

```bash
go test -bench=. -benchmem ./internal/store/
```

Run a specific benchmark by name:

```bash
go test -bench=BenchmarkAddTransaction -benchmem ./internal/store/
```

---

## Running

```bash
go run ./cmd/
```

The server listens on `:8080` by default. Override with the `PORT` environment variable:

```bash
PORT=9000 go run ./cmd/
```

---

## Manual execution

An end to end example:

```bash
# Create a GBP account
ACCOUNT=$(curl -s -X POST http://localhost:8080/accounts \
  -H 'Content-Type: application/json' \
  -d '{"currency":"GBP"}' | jq -r .id)

echo "Account ID: $ACCOUNT"

# Deposit 1000 (e.g. £10.00)
curl -s -X POST http://localhost:8080/accounts/$ACCOUNT/transactions \
  -H 'Content-Type: application/json' \
  -d '{"amount":1000,"currency":"GBP","transaction_date":"2024-01-01T09:00:00Z","description":"salary"}'

# Deposit another 500
curl -s -X POST http://localhost:8080/accounts/$ACCOUNT/transactions \
  -H 'Content-Type: application/json' \
  -d '{"amount":500,"currency":"GBP","transaction_date":"2024-01-02T09:00:00Z"}'

# Withdraw 200
curl -s -X POST http://localhost:8080/accounts/$ACCOUNT/transactions \
  -H 'Content-Type: application/json' \
  -d '{"amount":-200,"currency":"GBP","transaction_date":"2024-01-03T09:00:00Z","description":"coffee"}'

# Check balance — expect 1300
curl -s http://localhost:8080/accounts/$ACCOUNT/balance

# List all three transactions
curl -s http://localhost:8080/accounts/$ACCOUNT/transactions
```

Full API reference: [api_docs.md](api_docs.md). 

---

## Out of scope

The following were explicitly excluded by the assignment or deliberately omitted to simplify the implementation:

- **Authentication and authorisation** — any caller can read or write any account; explicitly out of scope per the assignment.
- **Persistence** — data is held in memory only and lost on restart; the assignment specifies in-memory storage.
- **Logging and monitoring** — no structured logging, metrics, or tracing; explicitly out of scope per the assignment.
- **Atomic transfers between accounts** — no transfer primitive; a transfer requires two separate transaction requests with no atomicity guarantee between them.
- **Point-in-time balance** — balance reflects all transactions to date; querying the balance as of a past date is not supported.
- **Transaction amendment or cancellation** — the transaction history is append-only; corrections require a new offsetting transaction.
- **Currency validation against ISO 4217** — currency codes are validated as three uppercase ASCII letters but not checked against the published list of valid codes.
- **Field-level size limits** — description length and other string fields are bounded only by the 1 MB request body cap.
- **Idempotency key eviction** — keys accumulate for the lifetime of the process; a production implementation would add TTL-based eviction.
- **Rate limiting** — no per-client or per-account request throttling.


## Design decisions

- **Amounts are integers in minor units** — avoids floating-point rounding errors.
- **Signed amounts encode direction** — positive = credit, negative = debit. Eliminates a redundant type field and the class of bugs where type and sign disagree.
- **Running balance** — each account maintains a current balance, updated on every transaction. This keeps balance lookups fast without aggregating transaction history each time.
- **Two timestamps per transaction** — one supplied by the caller (when the movement occurred) and one assigned by the server (when the record was written). Keeping them separate preserves the audit trail and makes date-range queries meaningful.
- **Transactions ordered by when they occurred** — transactions inserted out of chronological order are stored in the correct position. A database would handle this with an index.
- **Transaction list is paginated** — cursor-based pagination allows clients to page through results without missing or duplicating transactions if new ones are added between requests.
- **Transaction list can be filtered by date** — results can be bounded by an inclusive start and end date. Date filters and pagination compose correctly.
- **Write operations support idempotency** — an idempotency key can be supplied on any write request. Repeating a request with the same key returns the original response without re-executing the operation, making it safe to retry on network failure. Only successful responses are cached, so a failed request can be corrected and retried with the same key.
- **Thread-safe by design** — any combination of API calls may be made concurrently without corrupting state. Concurrent writes to the same account are serialised; operations on different accounts proceed independently.
- **API security is limited** — a request size limit is applied, but validation is otherwise limited to basic format checks.

## Assumptions

- **Multiple accounts are supported** — each account is independent with its own currency, balance, and transaction history.
- **Each account has a single currency** — currency is set at account creation and cannot be changed. All transactions must use the same currency as the account.
- **Accounts are created explicitly** — not implicitly on first transaction; allows zero-balance accounts.
- **Balances can go negative** — no overdraft floor; simplifies backdating.
- **Current balance only** — no point-in-time balance queries.
- **Zero-amount transactions are invalid** — an amount of zero has no financial meaning.
- **Transaction dates must not be in the future** — ensures the balance reflects only transactions that have already occurred.
- **Transaction history is immutable** — no void, amend, or delete; corrections are new transactions.
- **Newest-first ordering is fixed** — clients cannot change sort order.
- **Account and transaction volume unbounded** — growth limited only by process memory.

