package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestMetricsRecordsRequestAndExposesHandler(t *testing.T) {
	handler := Metrics()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/metrics-test-path", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(metricsRec, metricsReq)

	if metricsRec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want %d", metricsRec.Code, http.StatusOK)
	}
	if !strings.Contains(metricsRec.Body.String(), "http_requests_total") {
		t.Error("expected http_requests_total metric to be present in /metrics output")
	}
}

// TestMetricsUnmatchedRoutesShareOneLabel asserts that requests with no chi
// route context (as happens for unmatched/404 requests) are recorded under
// a single constant "unmatched" path label rather than the raw request
// path, so a client cannot inflate Prometheus label cardinality simply by
// requesting distinct nonexistent paths.
func TestMetricsUnmatchedRoutesShareOneLabel(t *testing.T) {
	handler := Metrics()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	junkPaths := []string{"/this-does-not-exist", "/another/junk/path?with=query"}
	for _, p := range junkPaths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status for %q = %d, want %d", p, rec.Code, http.StatusNotFound)
		}
	}

	metricsRec := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(metricsRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metricsRec.Body.String()

	if !strings.Contains(body, `path="unmatched"`) {
		t.Error(`expected metrics to contain path="unmatched" label`)
	}
	for _, p := range junkPaths {
		if strings.Contains(body, p) {
			t.Errorf("metrics output leaked raw request path %q; expected it to be collapsed to the unmatched label", p)
		}
	}
}
