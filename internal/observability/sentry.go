// Package observability wires the rize-backend process into Sentry for
// error tracking, per documentation/observability.md ("Per-project
// integration" > rize-backend / RIZ-53). It centralizes:
//
//   - SDK initialization from config (Init), including the "no DSN ->
//     Sentry cleanly disabled" local-dev path.
//   - Capture helpers used at the two seams documentation/observability.md
//     and the RIZ-53 brief call out: recovered panics
//     (internal/middleware.Recoverer) and 5xx responses written at the
//     internal/httpx.WriteError boundary.
//   - PII scrubbing (BeforeSend), per documentation/observability.md
//     §Privacy rules: "beforeSend scrubbers strip window_title, url, and
//     email addresses from events and breadcrumbs" and "User
//     identification in events is the pseudonymous user UUID only — never
//     email or display name." This package additionally strips
//     Authorization/auth-shaped headers and request bodies, since a Go
//     HTTP server (unlike the desktop/mobile clients the doc was written
//     against) routinely sees both.
//   - Flush, for bounded delivery of buffered events during graceful
//     shutdown.
//
// 4xx responses are never captured: they are expected, client-caused
// outcomes (bad input, auth failure, not-found), not backend faults worth
// alerting on. Only 5xx (server-fault) responses and panics are reported.
package observability

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// Config configures the Sentry SDK. It is built from environment variables
// by internal/config (SENTRY_DSN, ENVIRONMENT, SENTRY_RELEASE).
type Config struct {
	// DSN is the Sentry project DSN. Empty disables Sentry entirely: Init
	// becomes a no-op init (sentry-go's documented behavior for an empty
	// DSN — see its noopTransport) and every Capture* call below becomes
	// a safe no-op too, since they all go through the same disabled
	// client. This is the local-dev/default path.
	DSN string
	// Environment is tagged on every event (e.g. "development", "staging",
	// "production"), per documentation/observability.md ("Every event
	// carries environment and release tags").
	Environment string
	// Release is tagged on every event, per
	// documentation/observability.md. Sourced from the SENTRY_RELEASE
	// environment variable — see internal/config's doc comment on
	// SentryRelease for why an env var was chosen over an ldflags-injected
	// build variable (no such pattern exists elsewhere in this repo yet).
	Release string
}

// Init initializes the global Sentry client from cfg. It never returns an
// error for cfg.DSN == "" (Sentry is simply disabled); it can return an
// error if cfg.DSN is non-empty but malformed.
func Init(cfg Config) error {
	return sentry.Init(clientOptions(cfg))
}

// InitForTesting wires the Sentry SDK to transport instead of a real HTTP
// transport, so tests can assert on captured events without making network
// calls, while still exercising the exact same option set (including
// BeforeSend scrubbing) that Init uses. Production code must not call
// this; it exists for internal/observability, internal/middleware, and
// internal/httpx tests that verify capture/scrubbing behavior.
func InitForTesting(cfg Config, transport sentry.Transport) error {
	opts := clientOptions(cfg)
	opts.Transport = transport
	return sentry.Init(opts)
}

func clientOptions(cfg Config) sentry.ClientOptions {
	return sentry.ClientOptions{
		Dsn:         cfg.DSN,
		Environment: cfg.Environment,
		Release:     cfg.Release,
		// Per documentation/observability.md §Privacy rules:
		// "sendDefaultPii = false on every SDK."
		SendDefaultPII:   false,
		BeforeSend:       scrubEvent,
		AttachStacktrace: true,
	}
}

// Flush blocks until buffered Sentry events are delivered or timeout
// elapses, whichever comes first. Called during graceful shutdown so a
// panic/error reported just before shutdown isn't dropped. It is safe to
// call even when Sentry was never initialized or is disabled (DSN empty).
func Flush(timeout time.Duration) bool {
	return sentry.Flush(timeout)
}

// CapturePanic reports a recovered panic to Sentry, tagging it with the
// request's request ID. Callers are responsible for still handling the
// panic themselves (e.g. writing the 500 response) — this function only
// reports, it never itself panics or alters control flow.
func CapturePanic(ctx context.Context, recovered any) {
	hub := sentry.CurrentHub().Clone()
	hub.Scope().SetTag("request_id", chimw.GetReqID(ctx))
	hub.RecoverWithContext(ctx, recovered)
}

// CaptureHTTPError reports a 5xx error written at the httpx.WriteError
// seam to Sentry, tagging it with the request ID, status code, and error
// type URI. 4xx client errors are never reported (see package doc).
func CaptureHTTPError(ctx context.Context, status int, errType, title, detail string) {
	if status < 500 {
		return
	}

	hub := sentry.CurrentHub().Clone()
	hub.Scope().SetTag("request_id", chimw.GetReqID(ctx))
	hub.Scope().SetTag("http.status_code", strconv.Itoa(status))
	hub.Scope().SetTag("http.error_type", errType)
	hub.CaptureException(fmt.Errorf("%s: %s", title, detail))
}

// emailPattern matches an email address anywhere in a string, for
// redaction in event/breadcrumb text fields.
var emailPattern = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)

// redactedEmail replaces a matched email address in scrubbed text.
const redactedEmail = "[redacted-email]"

// scrubEvent is the BeforeSend hook applied to every outgoing event
// (errors and panics), implementing documentation/observability.md
// §Privacy rules for the backend:
//
//   - request bodies, cookies, URLs, and query strings are stripped
//     entirely (URLs/query strings can carry tokens or PII in path
//     segments or query params; the doc's "strip ... url ..." rule
//     applies here too);
//   - Authorization/auth/token-shaped headers are stripped;
//   - email addresses are redacted from messages, exception text, extras,
//     and breadcrumbs;
//   - user identification is reduced to the pseudonymous ID only (email,
//     username, and display name are cleared).
func scrubEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil {
		return nil
	}

	scrubRequest(event.Request)

	event.User.Email = ""
	event.User.Username = ""
	event.User.Name = ""

	event.Message = redactEmails(event.Message)
	for i := range event.Exception {
		event.Exception[i].Value = redactEmails(event.Exception[i].Value)
	}
	for _, ctx := range event.Contexts {
		scrubContext(ctx)
	}
	for _, b := range event.Breadcrumbs {
		scrubBreadcrumb(b)
	}

	return event
}

// scrubContext removes window_title/url entries (per
// documentation/observability.md's cross-project scrubber rule) and
// redacts email addresses from a Sentry "context" map's string values —
// e.g. the "extra" context, where ad hoc string data (like an unwrapped
// error's formatted message) commonly ends up.
func scrubContext(ctx sentry.Context) {
	delete(ctx, "url")
	delete(ctx, "window_title")
	for k, v := range ctx {
		if s, ok := v.(string); ok {
			ctx[k] = redactEmails(s)
		}
	}
}

// scrubRequest strips PII and secret-bearing fields from a captured HTTP
// request: the body, cookies, URL, query string, and any
// Authorization/auth/token-shaped header. Content-Type and other
// non-sensitive headers are left intact since they carry no PII.
func scrubRequest(req *sentry.Request) {
	if req == nil {
		return
	}
	req.Data = ""
	req.Cookies = ""
	req.URL = ""
	req.QueryString = ""
	for name := range req.Headers {
		if isSensitiveHeader(name) {
			delete(req.Headers, name)
		}
	}
}

// isSensitiveHeader reports whether a header name is a credential/auth
// carrier that must never reach Sentry, per
// documentation/observability.md §Privacy rules and the RIZ-53 brief
// ("never send ... tokens, Authorization/auth headers").
func isSensitiveHeader(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "authorization", "cookie", "set-cookie", "proxy-authorization":
		return true
	}
	return strings.Contains(lower, "auth") || strings.Contains(lower, "token")
}

// scrubBreadcrumb removes window_title/url breadcrumb data (per
// documentation/observability.md's cross-project scrubber rule) and
// redacts email addresses from the breadcrumb's message and remaining
// string data values.
func scrubBreadcrumb(b *sentry.Breadcrumb) {
	b.Message = redactEmails(b.Message)
	if b.Data == nil {
		return
	}
	delete(b.Data, "url")
	delete(b.Data, "window_title")
	for k, v := range b.Data {
		if s, ok := v.(string); ok {
			b.Data[k] = redactEmails(s)
		}
	}
}

// redactEmails replaces any email address found in s with a fixed
// placeholder.
func redactEmails(s string) string {
	if s == "" {
		return s
	}
	return emailPattern.ReplaceAllString(s, redactedEmail)
}
