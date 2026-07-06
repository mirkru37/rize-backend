package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// stubQuerier is a minimal storedb.Querier used only by this whitebox test
// (package auth, not auth_test) to spy on verifyDecoyPassword without
// reimplementing the full fakeQuerier that lives in the external
// fake_querier_test.go (package auth_test, and so inaccessible from here).
// It embeds a nil storedb.Querier so every method this test doesn't
// override is promoted and would panic if ever called — which is fine,
// since neither scenario below reaches past GetUserByEmail.
type stubQuerier struct {
	storedb.Querier
	getUserByEmail func(ctx context.Context, email *string) (storedb.User, error)
}

func (s stubQuerier) GetUserByEmail(ctx context.Context, email *string) (storedb.User, error) {
	return s.getUserByEmail(ctx, email)
}

func testSigningKeyInternal(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test RSA key: %v", err)
	}
	return key
}

// TestLogin_DecoyPasswordVerification asserts RIZ-32 M1's fix: every Login
// failure branch that would otherwise skip password verification entirely
// (unknown email, password-less/Apple-only account) must still call
// verifyDecoyPassword, so all failure paths pay comparable argon2id cost.
// This uses a spy (a swapped-in package-level var), not wall-clock timing,
// per the fix's explicit rejection of timing-based assertions as flaky.
func TestLogin_DecoyPasswordVerification(t *testing.T) {
	t.Run("unknown email", func(t *testing.T) {
		var calls int
		var gotPassword string
		orig := verifyDecoyPassword
		verifyDecoyPassword = func(password string) {
			calls++
			gotPassword = password
		}
		t.Cleanup(func() { verifyDecoyPassword = orig })

		svc := &Service{
			Queries: stubQuerier{
				getUserByEmail: func(_ context.Context, _ *string) (storedb.User, error) {
					return storedb.User{}, pgx.ErrNoRows
				},
			},
			SigningKey: testSigningKeyInternal(t),
		}

		_, err := svc.Login(context.Background(), "nobody@example.com", "whatever-password", DeviceInput{
			Platform:   "macos",
			Name:       "Test Device",
			Model:      "MacBookPro18,1",
			OSVersion:  "14.5",
			AppVersion: "1.0.0",
		})
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
		}
		if calls != 1 {
			t.Fatalf("verifyDecoyPassword call count = %d, want 1", calls)
		}
		if gotPassword != "whatever-password" {
			t.Fatalf("verifyDecoyPassword called with %q, want the caller-supplied password", gotPassword)
		}
	})

	t.Run("password-less (Apple-only) account", func(t *testing.T) {
		var calls int
		orig := verifyDecoyPassword
		verifyDecoyPassword = func(_ string) { calls++ }
		t.Cleanup(func() { verifyDecoyPassword = orig })

		svc := &Service{
			Queries: stubQuerier{
				getUserByEmail: func(_ context.Context, _ *string) (storedb.User, error) {
					return storedb.User{PasswordHash: nil}, nil
				},
			},
			SigningKey: testSigningKeyInternal(t),
		}

		_, err := svc.Login(context.Background(), "apple-user@example.com", "whatever-password", DeviceInput{
			Platform:   "macos",
			Name:       "Test Device",
			Model:      "MacBookPro18,1",
			OSVersion:  "14.5",
			AppVersion: "1.0.0",
		})
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
		}
		if calls != 1 {
			t.Fatalf("verifyDecoyPassword call count = %d, want 1", calls)
		}
	})

	t.Run("wrong password on a real account does not call the decoy path", func(t *testing.T) {
		// Sanity check on the spy itself: the wrong-password branch calls
		// VerifyPassword directly against the real stored hash, not the
		// decoy, so verifyDecoyPassword must not fire there.
		var calls int
		orig := verifyDecoyPassword
		verifyDecoyPassword = func(_ string) { calls++ }
		t.Cleanup(func() { verifyDecoyPassword = orig })

		hash, err := HashPassword("correct-horse-battery-staple")
		if err != nil {
			t.Fatalf("HashPassword: %v", err)
		}

		svc := &Service{
			Queries: stubQuerier{
				getUserByEmail: func(_ context.Context, _ *string) (storedb.User, error) {
					return storedb.User{PasswordHash: &hash}, nil
				},
			},
			SigningKey: testSigningKeyInternal(t),
		}

		_, err = svc.Login(context.Background(), "real-user@example.com", "wrong-password", DeviceInput{
			Platform:   "macos",
			Name:       "Test Device",
			Model:      "MacBookPro18,1",
			OSVersion:  "14.5",
			AppVersion: "1.0.0",
		})
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
		}
		if calls != 0 {
			t.Fatalf("verifyDecoyPassword call count = %d, want 0 (wrong-password branch verifies against the real hash)", calls)
		}
	})
}
