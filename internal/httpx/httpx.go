// Package httpx provides shared HTTP response helpers used by every
// handler: a plain JSON success-response writer and an RFC 7807-style
// "problem" error-response writer, per documentation/api-reference.md
// §Conventions. Both helpers echo the request's request ID (set by the
// request-ID middleware) back to the client via the X-Request-Id response
// header, satisfying the "every response echoes a request identifier"
// convention. api-reference.md leaves the exact echo mechanism as an open
// question; using a response header is this package's chosen
// interpretation.
package httpx

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// RequestIDHeader is the response header used to echo the request ID back
// to the client, per documentation/api-reference.md §Conventions ("every
// response echoes a request identifier").
const RequestIDHeader = "X-Request-Id"

// Problem is the RFC 7807-style error body used for every error response,
// per documentation/api-reference.md §Conventions.
type Problem struct {
	// Type is a URI identifying the error category.
	Type string `json:"type"`
	// Title is a short, human-readable summary of the error.
	Title string `json:"title"`
	// Status mirrors the HTTP status code.
	Status int `json:"status"`
	// Detail gives request-specific context.
	Detail string `json:"detail"`
}

// WriteJSON writes a successful JSON response with the given status code
// and payload, echoing the request ID (if present in the request context)
// via the X-Request-Id response header.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, payload any) {
	echoRequestID(w, r)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// WriteError writes an RFC 7807-style problem-body error response,
// echoing the request ID (if present in the request context) via the
// X-Request-Id response header.
func WriteError(w http.ResponseWriter, r *http.Request, status int, errType, title, detail string) {
	echoRequestID(w, r)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type:   errType,
		Title:  title,
		Status: status,
		Detail: detail,
	})
}

// echoRequestID sets the X-Request-Id response header from the request ID
// stashed in the request context by chi's RequestID middleware, if any.
func echoRequestID(w http.ResponseWriter, r *http.Request) {
	if r == nil {
		return
	}
	if id := middleware.GetReqID(r.Context()); id != "" {
		w.Header().Set(RequestIDHeader, id)
	}
}
