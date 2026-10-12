package xproapi

import (
	"errors"
	"fmt"
)

// One sentinel per kind of failure. Compare with errors.Is:
//
//	if errors.Is(err, xproapi.ErrNotFound) { ... }
var (
	// ErrBadRequest is a 400: a missing or invalid parameter.
	ErrBadRequest = errors.New("xproapi: bad request")
	// ErrAuthentication is a 401: missing or invalid X-API-Key.
	ErrAuthentication = errors.New("xproapi: authentication failed")
	// ErrInsufficientCredits is a 402: the balance can't cover the request. Nothing was charged.
	ErrInsufficientCredits = errors.New("xproapi: insufficient credits")
	// ErrPermissionDenied is a 403: the account can't use the API, or the X account is protected.
	ErrPermissionDenied = errors.New("xproapi: permission denied")
	// ErrNotFound is a 404: the user, tweet or article doesn't exist (or isn't visible).
	ErrNotFound = errors.New("xproapi: not found")
	// ErrRateLimit is a 429: too many requests. Error.RetryAfter says how many seconds to wait.
	ErrRateLimit = errors.New("xproapi: rate limited")
	// ErrServiceUnavailable is a 5xx: X or the API is temporarily unavailable. Safe to retry later.
	ErrServiceUnavailable = errors.New("xproapi: service unavailable")
)

// Error is any error answer from the API ({"status":"error","error":<code>,"message":"..."}) or a network failure.
// Use errors.As to read its fields, or errors.Is with one of the sentinels above to handle one case.
type Error struct {
	// Status is the HTTP status (0 for network errors).
	Status int
	// Message is the API's message.
	Message string
	// Body is the decoded JSON error body, when there was one.
	Body map[string]any
	// RetryAfter is how many seconds to wait before retrying (429 only, 0 when the API didn't say).
	RetryAfter int
	// Err is the underlying network error, when there was one.
	Err error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Err }

// Is reports whether the error is of the given kind (one of the Err… sentinels).
func (e *Error) Is(target error) bool {
	return target == sentinel(e.Status)
}

func sentinel(status int) error {
	switch {
	case status == 400:
		return ErrBadRequest
	case status == 401:
		return ErrAuthentication
	case status == 402:
		return ErrInsufficientCredits
	case status == 403:
		return ErrPermissionDenied
	case status == 404:
		return ErrNotFound
	case status == 429:
		return ErrRateLimit
	case status >= 500:
		return ErrServiceUnavailable
	}

	return nil
}

func errorFromResponse(status int, body map[string]any, retryAfter int) *Error {
	message, ok := body["message"].(string)
	if !ok {
		message = fmt.Sprintf("Request failed with HTTP %d.", status)
	}

	return &Error{Status: status, Message: message, Body: body, RetryAfter: retryAfter}
}
