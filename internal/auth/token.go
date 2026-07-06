package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Token lifetimes, pinned per documentation/security.md §Token model.
const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 30 * 24 * time.Hour
)

// AccessTokenClaims are the exact claims documentation/security.md §Token
// model specifies for the access token: "sub" (user id), "role", and
// "device_id", plus the standard registered claims (issued/expiry) needed to
// enforce the 15-minute lifetime. No sensitive data is embedded in the
// token.
type AccessTokenClaims struct {
	Role     string `json:"role"`
	DeviceID string `json:"device_id"`
	jwt.RegisteredClaims
}

// ErrInvalidAccessToken is returned by VerifyAccessToken for any
// malformed, expired, or invalid-signature token.
var ErrInvalidAccessToken = errors.New("auth: invalid access token")

// IssueAccessToken mints a signed RS256 access token for userID/role/deviceID
// per documentation/security.md §Token model, expiring AccessTokenTTL after
// now.
func IssueAccessToken(signingKey *rsa.PrivateKey, userID, role, deviceID string, now time.Time) (string, error) {
	claims := AccessTokenClaims{
		Role:     role,
		DeviceID: deviceID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(signingKey)
	if err != nil {
		return "", fmt.Errorf("auth: sign access token: %w", err)
	}
	return signed, nil
}

// VerifyAccessToken parses and verifies a signed access token, returning its
// claims. It rejects tokens signed with any algorithm other than RS256,
// tokens with an invalid signature, and expired tokens.
func VerifyAccessToken(publicKey *rsa.PublicKey, tokenString string) (*AccessTokenClaims, error) {
	claims := &AccessTokenClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("%w: unexpected signing method %v", ErrInvalidAccessToken, t.Header["alg"])
		}
		return publicKey, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAccessToken, err)
	}
	if !token.Valid {
		return nil, ErrInvalidAccessToken
	}
	return claims, nil
}

// refreshTokenPrefix marks a value as an opaque refresh token, matching the
// "rt_..." shape shown in documentation/api-reference.md's worked login
// example.
const refreshTokenPrefix = "rt_"

// refreshTokenRandomBytes is the amount of random entropy encoded into every
// opaque refresh token (256 bits), making the token itself unguessable and
// making a fast, unsalted hash (see HashRefreshToken) safe to use for
// storage/lookup.
const refreshTokenRandomBytes = 32

// GenerateRefreshToken returns a new opaque, high-entropy refresh token
// string. Refresh tokens are not JWTs and carry no claims of their own, per
// documentation/security.md §Token model.
func GenerateRefreshToken() (string, error) {
	b := make([]byte, refreshTokenRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate refresh token: %w", err)
	}
	return refreshTokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashRefreshToken returns the SHA-256 digest of a refresh token, as stored
// in refresh_tokens.token_hash per documentation/database-schema.md.
// Refresh tokens are generated with 256 bits of random entropy
// (GenerateRefreshToken), so — unlike passwords, which are low-entropy and
// require a slow, salted KDF (argon2id, used for password_hash) — a fast,
// unsalted cryptographic hash is the standard, appropriate choice here: it
// keeps token validation on every authenticated request a cheap indexed
// lookup (documentation/database-schema.md's stated rationale for the
// token_hash index) while still meaning the backend never needs to persist
// the plaintext token.
func HashRefreshToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
