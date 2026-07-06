package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters, pinned exactly per documentation/security.md
// §Authentication design / §Security checklist ("Passwords are hashed with
// argon2id at memory=64 MiB, iterations=3, parallelism=4"). These parameters
// apply uniformly to registration and to any future password reset/change
// flow.
const (
	argon2Memory      = 64 * 1024 // KiB (64 MiB)
	argon2Iterations  = 3
	argon2Parallelism = 4
	argon2SaltLength  = 16
	argon2KeyLength   = 32
)

// ErrInvalidPasswordHash is returned by VerifyPassword when the stored hash
// is not in the expected encoded format.
var ErrInvalidPasswordHash = errors.New("auth: invalid password hash encoding")

// HashPassword hashes a plaintext password with argon2id using the
// parameters pinned in documentation/security.md, returning a
// self-describing encoded string (algorithm, parameters, salt, and hash all
// included) suitable for storage in users.password_hash. A plaintext
// password is never stored or compared in any other form, per
// documentation/security.md.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argon2Iterations, argon2Memory, argon2Parallelism, argon2KeyLength)

	encoded := fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argon2Memory,
		argon2Iterations,
		argon2Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)
	return encoded, nil
}

// VerifyPassword reports whether password matches the given argon2id-encoded
// hash produced by HashPassword, using a constant-time comparison of the
// derived keys.
func VerifyPassword(encodedHash, password string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidPasswordHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, ErrInvalidPasswordHash
	}

	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, ErrInvalidPasswordHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidPasswordHash
	}

	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidPasswordHash
	}

	if len(want) <= 0 || len(want) > 1<<16 {
		// Sanity bound before the int -> uint32 conversion below; a
		// correctly-encoded hash from HashPassword is always
		// argon2KeyLength (32) bytes.
		return false, ErrInvalidPasswordHash
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want))) //nolint:gosec // bounds-checked above

	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// decoyPasswordHash is a fixed, precomputed argon2id hash (using the same
// parameters as HashPassword) with no corresponding real account. It exists
// purely so failure branches that would otherwise skip password
// verification entirely can still pay a comparable argon2id cost — see
// verifyDecoyPassword.
var decoyPasswordHash = mustHashDecoyPassword()

func mustHashDecoyPassword() string {
	// The actual passphrase is arbitrary and never compared against a real
	// password; only the resulting hash's shape/cost matters.
	hash, err := HashPassword("rize-clone-fixed-decoy-password-for-timing-parity")
	if err != nil {
		panic(fmt.Sprintf("auth: failed to precompute decoy password hash: %v", err))
	}
	return hash
}

// verifyDecoyPassword pays the same argon2id verification cost as a real
// password check, without revealing anything about whether an account
// exists, by comparing the caller-supplied password against a fixed decoy
// hash and discarding the result. Login calls this on every failure branch
// that would otherwise return ErrInvalidCredentials without ever calling
// VerifyPassword (unknown email, password-less/Apple-only account), so
// every failure path pays comparable argon2 cost to the
// wrong-password-mismatch branch and doesn't leak account existence via
// response timing, per documentation/security.md's account-enumeration
// hardening intent (RIZ-32 M1).
//
// It is a package-level var (rather than a direct call to VerifyPassword)
// so whitebox tests can install a spy to assert this path was actually
// exercised — timing-based assertions were explicitly rejected as flaky for
// this fix.
var verifyDecoyPassword = func(password string) {
	_, _ = VerifyPassword(decoyPasswordHash, password)
}
