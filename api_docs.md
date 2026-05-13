# Ledger API Reference

Base URL: `http://localhost:8080`

All request and response bodies are JSON. Amounts are integers in **minor currency units** (e.g. pence, cents). Timestamps are RFC 3339 UTC.

---

## Error responses

All errors return the same envelope:

```json
{ "error": "<human-readable message>" }
```

| Status | Condition |
|--------|-----------|
| `400 Bad Request` | Missing or invalid field in the request body |
| `404 Not Found` | Account ID does not exist |
| `415 Unsupported Media Type` | `POST` request missing `Content-Type: application/json` |
| `409 Conflict` | Transaction session state conflict — begin when a session is already open, or commit/rollback when no session is open |
| `422 Unprocessable Entity` | Transaction would overflow the account balance, or currency does not match the account |
| `500 Internal Server Error` | Unexpected server error — used for any store error that is not a known sentinel. Not expected in normal operation with the in-memory backend; a future persistence backend may return 500 for I/O failures. Extremely rare OS-level failures (e.g. `crypto/rand` unavailability) could also produce a 500. |

---

## Idempotency

`POST /accounts/{id}/transactions` supports idempotent requests via the `Idempotency-Key` header. This allows clients to safely retry a request after a network failure without risk of creating duplicate transactions.

**How it works**

Supply a client-generated UUID v4 as the `Idempotency-Key` header. Non-UUID values are rejected with 400.

```
POST /accounts/{id}/transactions
Idempotency-Key: a8098c1a-f86e-11da-bd1a-00112444be1e
Content-Type: application/json

{ "amount": 1000, "transaction_date": "2024-01-13T09:00:00Z" }
```

- **First request** — processed normally. The successful `201` response is cached against the key.
- **Subsequent requests with the same key** — the cached `201` response is returned immediately; the transaction is not recorded again.
- **Error responses are not cached** — if the first request fails (e.g. 400, 404), the key remains unused. The client may retry with the same key and a corrected request.

**Key requirements**

- Must be a valid UUID (any version). The server validates format and rejects non-UUIDs with 400.
- Should be unique per intended transaction — generate a fresh UUID v4 client-side for each new transaction attempt.
- Omitting the header disables idempotency for that request; two requests without a key always create two transactions.

**Scope and lifetime**

Keys are scoped to the server process. They are not persisted — a server restart clears all cached responses. In the current in-memory implementation, cached responses are retained for the lifetime of the process with no expiry.

---

## Accounts

### Create account

```
POST /accounts
```

Creates a new account with a given currency. The server assigns the ID.

**Request body**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `currency` | string | yes | ISO 4217 currency code (3 uppercase letters, e.g. `GBP`, `USD`). |

```json
{ "currency": "GBP" }
```

**Response `201 Created`**

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | UUID assigned by the server |
| `currency` | string | ISO 4217 currency code |
| `created_at` | string | ISO 8601 timestamp |

```json
{
  "id": "4a2f1b3c-8e6d-4f2a-b1c0-9d7e5f3a2b1c",
  "currency": "GBP",
  "created_at": "2024-01-15T10:00:00Z"
}
```

---

## Balance

### Get balance

```
GET /accounts/{id}/balance
```

Returns the current balance for an account from a maintained running total (O(1) lookup).

**Path parameters**

| Parameter | Description |
|-----------|-------------|
| `id` | Account UUID |

**Response `200 OK`**

| Field | Type | Description |
|-------|------|-------------|
| `account_id` | string | Account UUID |
| `balance` | integer | Current balance in minor units. May be negative. |

```json
{
  "account_id": "4a2f1b3c-8e6d-4f2a-b1c0-9d7e5f3a2b1c",
  "balance": 1300
}
```

---

## Transactions

### Record a transaction

```
POST /accounts/{id}/transactions
```

Records a money movement against the account. A positive `amount` is a credit (deposit); a negative `amount` is a debit (withdrawal). The balance may go negative.

**Path parameters**

| Parameter | Description |
|-----------|-------------|
| `id` | Account UUID |

**Request body**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `amount` | integer | yes | Non-zero amount in minor units. Positive = credit, negative = debit. |
| `currency` | string | yes | ISO 4217 currency code. Must match the account's currency; mismatches are rejected with 422. |
| `description` | string | no | Free-text note (e.g. `"salary"`, `"coffee"`) |
| `transaction_date` | RFC 3339 timestamp | yes | When the movement occurred. Must not be in the future; past dates are accepted freely. |

```json
{
  "amount": 1000,
  "currency": "GBP",
  "description": "salary",
  "transaction_date": "2024-01-13T09:00:00Z"
}
```

**Response `201 Created`**

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | UUID for this transaction |
| `account_id` | string | Account UUID |
| `currency` | string | ISO 4217 currency code |
| `amount` | integer | Signed amount in minor units. Positive = credit, negative = debit. |
| `description` | string | Free-text note, omitted if empty |
| `transaction_date` | string | When the movement occurred (client-supplied or server time) |
| `created_at` | string | When the record was written — always server-assigned, never client-controllable |

```json
{
  "id": "7c9e2d1a-3f5b-4e8c-a2d0-1b6f4e7c9a3d",
  "account_id": "4a2f1b3c-8e6d-4f2a-b1c0-9d7e5f3a2b1c",
  "amount": 1000,
  "description": "salary",
  "transaction_date": "2024-01-13T09:00:00Z",
  "created_at": "2024-01-15T10:01:00Z"
}
```

---

### List transactions

```
GET /accounts/{id}/transactions
```

Returns transactions for an account in reverse chronological order (newest first). Supports cursor-based pagination via `limit` and `after`.

**Path parameters**

| Parameter | Description |
|-----------|-------------|
| `id` | Account UUID |

**Query parameters**

All parameters are optional.

| Parameter | Type | Description |
|-----------|------|-------------|
| `since` | RFC 3339 timestamp | Return only transactions at or after this time (e.g. `2024-01-15T10:00:00Z`). |
| `until` | RFC 3339 timestamp | Return only transactions at or before this time. |
| `after` | string | Cursor: return transactions after this transaction ID (exclusive). Use `next_cursor` from a previous response to paginate. |
| `limit` | integer | Maximum number of transactions to return. Omit to return all matching transactions. Must be a positive integer if provided; `0` is rejected. |

**Response `200 OK`**

| Field | Type | Description |
|-------|------|-------------|
| `account_id` | string | Account UUID |
| `transactions` | array | Ordered list of transactions (empty array if none) |
| `next_cursor` | string | ID of the oldest transaction on the current page. Present only when `limit` was specified and older transactions remain. Pass as `?after=` to fetch the next (older) page. |

Each element of `transactions` has the same shape as the response from [Record a transaction](#record-a-transaction).

**Example — first page**

```
GET /accounts/4a2f1b3c-.../transactions?limit=2
```

```json
{
  "account_id": "4a2f1b3c-8e6d-4f2a-b1c0-9d7e5f3a2b1c",
  "transactions": [
    {
      "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "account_id": "4a2f1b3c-8e6d-4f2a-b1c0-9d7e5f3a2b1c",
      "amount": -200,
      "description": "coffee",
      "transaction_date": "2024-01-15T10:02:00Z",
      "created_at": "2024-01-15T10:02:00Z"
    },
    {
      "id": "7c9e2d1a-3f5b-4e8c-a2d0-1b6f4e7c9a3d",
      "account_id": "4a2f1b3c-8e6d-4f2a-b1c0-9d7e5f3a2b1c",
      "amount": 1000,
      "description": "salary",
      "transaction_date": "2024-01-15T10:01:00Z",
      "created_at": "2024-01-15T10:01:00Z"
    }
  ],
  "next_cursor": "7c9e2d1a-3f5b-4e8c-a2d0-1b6f4e7c9a3d"
}
```

**Example — next page**

```
GET /accounts/4a2f1b3c-.../transactions?limit=2&after=7c9e2d1a-3f5b-4e8c-a2d0-1b6f4e7c9a3d
```

When the last page is returned, `next_cursor` is omitted. If `limit` is not supplied at all, `next_cursor` is never set — pagination requires an explicit `limit`.

**Note on cursor + date filter interaction:** `next_cursor` is the ID of the oldest transaction on the current page. If you pass this cursor alongside a `since` or `until` filter on the next request, the cursor establishes the starting position first and the date filter is then applied to the remaining items. This can produce an empty result if the cursor transaction sits at the `since` boundary — this is expected behaviour, not an error.

---

## Transaction sessions

A transaction session lets you stage multiple transactions and apply them atomically. While a session is open, staged transactions are invisible — they do not appear in the balance or transaction list. On commit, all staged transactions are applied together in a single atomic step. On rollback, they are discarded entirely.

Only one session may be open per account at a time. Sessions are in-memory only and are lost on server restart.

---

### Begin session

```
POST /accounts/{id}/begin
```

Opens a transaction session. All subsequent [Record a transaction](#record-a-transaction) calls on this account will stage the transaction rather than applying it immediately.

**Path parameters**

| Parameter | Description |
|-----------|-------------|
| `id` | Account UUID |

**Response `200 OK`**

```json
{}
```

**Errors**

| Status | Condition |
|--------|-----------|
| `400 Bad Request` | Account ID is not a valid UUID |
| `404 Not Found` | Account does not exist |
| `409 Conflict` | A session is already open for this account |

---

### Commit session

```
POST /accounts/{id}/commit
```

Atomically applies all staged transactions. The balance and transaction list are updated in a single step — no partial state is ever visible. If any staged transaction would cause the balance to overflow, the entire commit is rejected and the session remains open so the client can correct and retry, or call rollback.

**Path parameters**

| Parameter | Description |
|-----------|-------------|
| `id` | Account UUID |

**Response `200 OK`**

```json
{}
```

**Errors**

| Status | Condition |
|--------|-----------|
| `400 Bad Request` | Account ID is not a valid UUID |
| `404 Not Found` | Account does not exist |
| `409 Conflict` | No session is open for this account |
| `422 Unprocessable Entity` | Staged transactions would overflow the balance; session remains open |

---

### Rollback session

```
POST /accounts/{id}/rollback
```

Discards all staged transactions. The account balance and transaction list are completely unchanged. A new session may be opened immediately afterwards.

**Path parameters**

| Parameter | Description |
|-----------|-------------|
| `id` | Account UUID |

**Response `200 OK`**

```json
{}
```

**Errors**

| Status | Condition |
|--------|-----------|
| `400 Bad Request` | Account ID is not a valid UUID |
| `404 Not Found` | Account does not exist |
| `409 Conflict` | No session is open for this account |

---

### Example flow

```bash
# Open a session
POST /accounts/4a2f1b3c-.../begin                          → 200 {}

# Stage two transactions (neither is reflected yet)
POST /accounts/4a2f1b3c-.../transactions                   → 201
{ "amount": 500, "currency": "GBP", "transaction_date": "2024-01-15T10:00:00Z" }

POST /accounts/4a2f1b3c-.../transactions                   → 201
{ "amount": -100, "currency": "GBP", "transaction_date": "2024-01-15T10:01:00Z" }

# Balance is still frozen at its pre-session value
GET  /accounts/4a2f1b3c-.../balance                        → 200 { "balance": 0 }

# Commit: both transactions applied atomically
POST /accounts/4a2f1b3c-.../commit                         → 200 {}

GET  /accounts/4a2f1b3c-.../balance                        → 200 { "balance": 400 }
```
