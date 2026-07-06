package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

// RIZ-32 assumption: documentation/security.md §Token model explicitly
// leaves the access-token signing algorithm as an open question ("RS256 or
// EdDSA... should be pinned to a single algorithm... before implementation").
// This ticket pins it to RS256, since config.Config.JWTSigningKey (added by
// RIZ-30) is documented as "deliberately untyped key material (e.g. a
// PEM-encoded key)" rather than a fixed-format HMAC secret, which fits an
// asymmetric PEM-encoded RSA private key. A future ticket can revisit this
// choice (and add JWKS publishing) without changing the wire contract of the
// tokens issued here, since access tokens are opaque to clients beyond their
// claims.

// LoadSigningKey parses a PEM-encoded RSA private key (PKCS#1 or PKCS#8) from
// pemData, as read from config.Config.JWTSigningKey. It returns an error if
// pemData is empty or does not decode to an RSA private key.
func LoadSigningKey(pemData string) (*rsa.PrivateKey, error) {
	if pemData == "" {
		return nil, fmt.Errorf("auth: JWT signing key is empty")
	}

	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, fmt.Errorf("auth: JWT signing key is not valid PEM")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("auth: parse JWT signing key: %w", err)
	}

	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("auth: JWT signing key is not an RSA private key")
	}
	return rsaKey, nil
}

// GenerateSigningKey generates a fresh, ephemeral RSA-2048 private key. It is
// used as a development-only fallback (see config.Config.Environment) when no
// JWT_SIGNING_KEY is configured, so a fresh checkout can run the server
// without additional setup; it must never be used outside "development"
// since keys are not persisted across restarts and every restart invalidates
// all previously issued access tokens.
func GenerateSigningKey() (*rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("auth: generate ephemeral JWT signing key: %w", err)
	}
	return key, nil
}
