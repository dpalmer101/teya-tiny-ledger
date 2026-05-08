package api

import (
	"bytes"
	"net/http"
	"sync"
)

const headerIdempotencyKey = "Idempotency-Key"

type responseCapture struct {
	http.ResponseWriter
	status  int
	headers http.Header
	body    bytes.Buffer
}

func (rc *responseCapture) WriteHeader(status int) {
	rc.status = status
	rc.headers = rc.ResponseWriter.Header().Clone()
	rc.ResponseWriter.WriteHeader(status)
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	rc.body.Write(b)
	return rc.ResponseWriter.Write(b)
}

type cachedResponse struct {
	status  int
	headers http.Header
	body    []byte
}

type idempotencyEntry struct {
	mu       sync.Mutex
	response *cachedResponse // nil until a successful response has been stored
}

type Idempotency struct {
	cache sync.Map // string → *idempotencyEntry
}

// Wrap returns a handler that honours the Idempotency-Key request header.
//
//   - Requests without the header pass through to next unchanged.
//   - The first request with a given key is forwarded to next; a successful
//     (2xx) response is captured and cached against the key.
//   - Any later request with the same key receives the cached response without
//     calling next again.
//   - Non-2xx responses are not cached so the caller may retry with the same key.
func (id *Idempotency) Wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(headerIdempotencyKey)
		if key == "" {
			next(w, r)
			return
		}
		if !validID(key) {
			writeErrorString(w, http.StatusBadRequest, "Idempotency-Key must be a valid UUID (e.g. f47ac10b-58cc-4372-a567-0e02b2c3d479)")
			return
		}

		entry := &idempotencyEntry{}
		actual, _ := id.cache.LoadOrStore(key, entry)
		e := actual.(*idempotencyEntry)

		e.mu.Lock()
		defer e.mu.Unlock()

		if e.response != nil {
			replayResponse(w, e.response)
			return
		}

		rc := &responseCapture{ResponseWriter: w, status: http.StatusOK}
		next(rc, r)

		if rc.status >= 200 && rc.status < 300 {
			e.response = &cachedResponse{
				status:  rc.status,
				headers: rc.headers,
				body:    rc.body.Bytes(),
			}
		}
		// Non-2xx: e.response stays nil; the next request with the same key retries.
	}
}

func replayResponse(w http.ResponseWriter, resp *cachedResponse) {
	for k, vs := range resp.headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.status)
	w.Write(resp.body) //nolint:errcheck
}
