package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecovererCatchesPanic(t *testing.T) {
	handler := Recoverer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	// Should not panic out of ServeHTTP.
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
