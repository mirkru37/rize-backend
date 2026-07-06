package auth

import "errors"

// Sentinel errors returned by Service methods. Handlers (internal/auth's
// HTTP layer) map these to the RFC 7807-style Problem responses documented
// in documentation/api-reference.md §Conventions; the service layer itself
// stays free of any HTTP-specific concerns per rize-backend/CLAUDE.md's
// "handlers -> services -> repositories" layering rule.
var (
	// ErrEmailTaken is returned by Register when the requested email is
	// already registered.
	ErrEmailTaken = errors.New("auth: email already registered")

	// ErrInvalidCredentials is returned by Login when the email/password
	// combination does not match a valid, password-authenticated account.
	// It deliberately does not distinguish "no such user" from "wrong
	// password", per documentation/security.md's account-enumeration
	// hardening intent.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")

	// ErrInvalidRefreshToken is returned by Refresh and Logout when the
	// presented refresh token does not resolve to an active, unexpired
	// token.
	ErrInvalidRefreshToken = errors.New("auth: invalid refresh token")

	// ErrRefreshTokenReuse is returned by Refresh when an
	// already-rotated (or revoked) refresh token is presented again,
	// signaling likely token theft per documentation/security.md's
	// reuse-detection flow. By the time this error is returned, the
	// entire token family has already been revoked.
	ErrRefreshTokenReuse = errors.New("auth: refresh token reuse detected")

	// ErrDeviceNotFound is returned when a device lookup scoped to the
	// caller's user_id finds no matching, non-revoked device.
	ErrDeviceNotFound = errors.New("auth: device not found")

	// ErrUserNotFound is returned when a user lookup finds no matching,
	// non-deleted user.
	ErrUserNotFound = errors.New("auth: user not found")

	// ErrValidation is returned for malformed or out-of-range request
	// payloads, per documentation/security.md §API hardening ("input
	// validation is applied to every payload on every route").
	ErrValidation = errors.New("auth: validation error")
)
