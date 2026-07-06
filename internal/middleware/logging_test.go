package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoggingDoesNotCrashAndLogsRequest(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	handler := RequestID(Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})))

	req := httptest.NewRequest(http.MethodGet, "/foo/bar", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}

	out := buf.String()
	if !strings.Contains(out, `"method":"GET"`) {
		t.Errorf("log output missing method field: %s", out)
	}
	if !strings.Contains(out, `"path":"/foo/bar"`) {
		t.Errorf("log output missing path field: %s", out)
	}
	if !strings.Contains(out, `"status":418`) {
		t.Errorf("log output missing status field: %s", out)
	}
}
