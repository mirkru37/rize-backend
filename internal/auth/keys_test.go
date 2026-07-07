package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func pemEncodePKCS1(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	return string(pem.EncodeToMemory(block))
}

func pemEncodePKCS8(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block))
}

func TestLoadSigningKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}

	tests := []struct {
		name    string
		pemData string
		wantErr bool
	}{
		{name: "empty input", pemData: "", wantErr: true},
		{name: "not valid PEM", pemData: "not pem data at all", wantErr: true},
		{name: "PKCS1-encoded RSA key", pemData: pemEncodePKCS1(t, key), wantErr: false},
		{name: "PKCS8-encoded RSA key", pemData: pemEncodePKCS8(t, key), wantErr: false},
		{
			name:    "PEM block with malformed DER",
			pemData: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not-a-real-key-der")})),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadSigningKey(tt.pemData)
			if (err != nil) != tt.wantErr {
				t.Fatalf("LoadSigningKey() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if got == nil {
					t.Fatal("expected a non-nil key")
				}
				if got.N.Cmp(key.N) != 0 {
					t.Error("parsed key does not match the original key's modulus")
				}
			}
		})
	}
}

func TestLoadSigningKeyRejectsNonRSAPKCS8Key(t *testing.T) {
	// An EC key, PKCS8-encoded, decodes fine via x509.ParsePKCS8PrivateKey but
	// is not an *rsa.PrivateKey, so LoadSigningKey must reject it rather than
	// panic on the type assertion.
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pemData := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	if _, err := LoadSigningKey(pemData); err == nil {
		t.Fatal("expected an error for a non-RSA PKCS8 key, got nil")
	}
}

func TestGenerateSigningKey(t *testing.T) {
	key1, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	if key1 == nil {
		t.Fatal("expected a non-nil key")
	}
	if bits := key1.N.BitLen(); bits < 2040 {
		t.Errorf("key size = %d bits, want ~2048", bits)
	}

	key2, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey (second call): %v", err)
	}
	if key1.N.Cmp(key2.N) == 0 {
		t.Error("two calls to GenerateSigningKey produced the same key; expected fresh ephemeral keys")
	}
}
