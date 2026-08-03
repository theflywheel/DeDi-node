package publisher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

type ctxKey struct{}

// MaxBodyBytes caps a signed request body. The body must be buffered whole to
// verify the digest, so an uncapped read would let an unauthenticated caller
// spend the node's memory before any signature is checked.
const MaxBodyBytes = 1 << 20 // 1 MiB

// Authenticator verifies signed writes for a handler chain.
type Authenticator struct {
	Keys    *KeySet
	MaxSkew time.Duration // zero means DefaultMaxSkew
	Now     func() time.Time
}

func (a *Authenticator) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Authenticator) skew() time.Duration {
	if a.MaxSkew > 0 {
		return a.MaxSkew
	}
	return DefaultMaxSkew
}

// Require wraps h so it only runs for correctly signed requests. On success the
// verified key is available via KeyFrom, and the body is restored for the
// handler to read normally.
//
// deny receives every rejection so the caller can render errors in its own
// response format rather than this package inventing one.
func (a *Authenticator) Require(h http.Handler, deny func(http.ResponseWriter, *http.Request, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
		if err != nil {
			deny(w, r, err)
			return
		}
		r.Body.Close()

		// The signature covers the path as requested, before any rewriting.
		key, err := a.Keys.Verify(r.Method, r.URL.EscapedPath(), body,
			r.Header.Get(HeaderKeyID), r.Header.Get(HeaderTimestamp), r.Header.Get(HeaderSignature),
			a.now(), a.skew())
		if err != nil {
			deny(w, r, err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, key)))
	})
}

// KeyFrom returns the publisher key that signed the request.
func KeyFrom(ctx context.Context) (Key, bool) {
	k, ok := ctx.Value(ctxKey{}).(Key)
	return k, ok
}

// StatusFor maps a verification failure to an HTTP status. Everything that is
// an authentication failure is 401; a well-signed request by a key with no
// authority over the target namespace is 403, because retrying with the same
// credential will never succeed.
func StatusFor(err error) int {
	switch {
	case errors.Is(err, ErrWrongScope):
		return http.StatusForbidden
	case errors.Is(err, ErrMalformedHead):
		return http.StatusBadRequest
	case errors.Is(err, ErrNoSignature), errors.Is(err, ErrUnknownKey),
		errors.Is(err, ErrBadSignature), errors.Is(err, ErrStale):
		return http.StatusUnauthorized
	default:
		return http.StatusBadRequest
	}
}
