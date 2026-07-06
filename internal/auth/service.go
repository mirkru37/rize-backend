// Package auth implements authentication and account/device management for
// rize-backend: email/password registration and login, JWT access-token
// issuance/verification, rotating opaque refresh tokens with family-based
// reuse detection, device registration, and the user-profile and device
// management operations layered on top of the same identity — per
// documentation/security.md (the token/auth contract) and
// documentation/api-reference.md §Auth/§Users/§Devices (the wire contract).
//
// Sign in with Apple (POST /v1/auth/apple) and the password reset flow are
// explicitly out of scope for this ticket (RIZ-32) and are served as 501 Not
// Implemented stubs; see stubs.go.
package auth

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// uniqueViolation is the Postgres SQLSTATE for a UNIQUE constraint
// violation, used to detect a duplicate email on registration.
const uniqueViolationSQLState = "23505"

// DeviceInput is the client-supplied device metadata accepted by
// register/login (always) and refresh (optionally, to refresh metadata for
// the device already bound to the presented refresh token), per
// documentation/security.md §Token model ("a device row is created/updated
// and bound to the refresh token").
//
// RIZ-32 assumption: documentation/api-reference.md does not specify the
// exact device-registration request shape. ID is an optional
// previously-issued device id (returned by a prior register/login/refresh
// call as Device.ID) that lets a client reconnect to its existing device row
// instead of creating a new one on every login; if empty, or if it does not
// resolve to a live device owned by the account being authenticated, a new
// device row is created.
type DeviceInput struct {
	ID         string
	Platform   string
	Name       string
	Model      string
	OSVersion  string
	AppVersion string
}

func (d DeviceInput) validate() error {
	switch d.Platform {
	case "macos", "ios":
	default:
		return fmt.Errorf("%w: device.platform must be one of \"macos\", \"ios\"", ErrValidation)
	}
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("%w: device.name is required", ErrValidation)
	}
	if strings.TrimSpace(d.Model) == "" {
		return fmt.Errorf("%w: device.model is required", ErrValidation)
	}
	if strings.TrimSpace(d.OSVersion) == "" {
		return fmt.Errorf("%w: device.os_version is required", ErrValidation)
	}
	if strings.TrimSpace(d.AppVersion) == "" {
		return fmt.Errorf("%w: device.app_version is required", ErrValidation)
	}
	return nil
}

// TokenPair is the access/refresh token pair issued on register, login, and
// refresh, per documentation/api-reference.md's worked login example.
type TokenPair struct {
	AccessToken     string
	RefreshToken    string
	AccessExpiresIn int64 // seconds
}

// Result bundles everything a register/login/refresh response needs.
type Result struct {
	User   storedb.User
	Device storedb.Device
	Tokens TokenPair
}

// Service implements the auth business logic described in the package doc
// comment. It depends only on storedb.Querier (not the concrete *Queries),
// so tests can substitute an in-memory fake (see querier_test.go).
type Service struct {
	Queries    storedb.Querier
	SigningKey *rsa.PrivateKey
	Now        func() time.Time
}

func newResult(user storedb.User, device storedb.Device, accessToken, refreshToken string) Result {
	return Result{
		User:   user,
		Device: device,
		Tokens: TokenPair{
			AccessToken:     accessToken,
			RefreshToken:    refreshToken,
			AccessExpiresIn: int64(AccessTokenTTL.Seconds()),
		},
	}
}

// now returns s.Now() if set, otherwise time.Now(). A nil Now func is only
// expected in zero-value/misconfigured Services; production wiring always
// sets it.
func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func validEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return "", fmt.Errorf("%w: email is required", ErrValidation)
	}
	addr, err := mail.ParseAddress(email)
	if err != nil {
		return "", fmt.Errorf("%w: email is not a valid address", ErrValidation)
	}
	return addr.Address, nil
}

func validPassword(password string) error {
	if len(password) < 8 {
		return fmt.Errorf("%w: password must be at least 8 characters", ErrValidation)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationSQLState
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timestamptzNow(now time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: now, Valid: true}
}
