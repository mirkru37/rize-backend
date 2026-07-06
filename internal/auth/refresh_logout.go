package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// Refresh validates and rotates a presented refresh token, per
// documentation/security.md's refresh-token rotation flow:
//
//   - unknown token -> ErrInvalidRefreshToken
//   - expired token -> ErrInvalidRefreshToken
//   - already-rotated/revoked token presented again -> the entire token
//     family is revoked and ErrRefreshTokenReuse is returned (reuse
//     detection)
//   - otherwise: the presented token is consumed, a new token in the same
//     family is issued, and a new access token is minted
//
// device is optional device metadata to refresh on the device row already
// bound to the presented token (see documentation/security.md §Token model:
// "a device row is created/updated ... at login/refresh").
//
// When s.Pool is set (production), the whole lookup -> rotate -> consume
// sequence runs inside a single database transaction that row-locks the
// presented token, so concurrent refreshes of the same token serialize
// against each other instead of racing — see refreshTx (RIZ-32 M2). When
// s.Pool is nil (unit tests substituting a fakeQuerier, which has no notion
// of concurrent transactions), refreshNoTx runs the same sequence directly
// against s.Queries.
func (s *Service) Refresh(ctx context.Context, refreshToken string, device *DeviceInput) (Result, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return Result{}, ErrInvalidRefreshToken
	}

	if s.Pool != nil {
		return s.refreshTx(ctx, refreshToken, device)
	}
	return s.refreshNoTx(ctx, refreshToken, device)
}

// refreshTx implements Refresh's lookup -> rotate -> consume sequence
// inside a single pgx.Tx, per RIZ-32 M2:
//
//  1. It first attempts to lock the presented token's row with
//     `SELECT ... FOR UPDATE NOWAIT`. Postgres either grants the lock
//     immediately (no other transaction is touching this row right now) or
//     fails immediately with SQLSTATE 55P03 (lock_not_available) if another
//     transaction currently holds it.
//  2. If NOWAIT reports contention, it falls back to a blocking
//     `SELECT ... FOR UPDATE`, which waits for the concurrently in-flight
//     rotation to commit and then returns the now-serialized final row.
//     This request is the "loser" of a race for this exact token: it
//     observes the row already consumed by the winner and returns a clean
//     ErrInvalidRefreshToken (401), *without* revoking the token family —
//     losing a race with a legitimate rotation of the same token is not
//     evidence of token theft.
//  3. If no contention was ever observed (the NOWAIT lock was granted
//     immediately) and the row is already revoked, that revocation must
//     have come from a transaction that fully committed before this
//     request even started looking at the row — i.e. genuine reuse of an
//     already-rotated token, per documentation/security.md's
//     reuse-detection flow. The entire family is revoked.
//  4. Otherwise the token is live: the replacement token is created and the
//     presented one is consumed in the same transaction, so a failure
//     partway through rolls back everything (no orphaned replacement row is
//     ever left behind).
func (s *Service) refreshTx(ctx context.Context, refreshToken string, device *DeviceInput) (Result, error) {
	hash := HashRefreshToken(refreshToken)

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("auth: begin refresh transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	txQueries := storedb.New(tx)

	// The NOWAIT lock attempt below is tried inside its own savepoint
	// (pgx's Tx.Begin on an existing Tx issues SAVEPOINT / ROLLBACK TO
	// SAVEPOINT / RELEASE SAVEPOINT under the hood): a lock_not_available
	// error aborts whatever Postgres (sub-)transaction it happened in, so
	// without a savepoint to roll back to, the *entire* outer transaction
	// would be poisoned (every later statement failing with 25P02) rather
	// than letting us fall back to a blocking wait within the same tx.
	contended := false
	lockAttempt, err := tx.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("auth: begin lock-attempt savepoint: %w", err)
	}
	current, err := storedb.New(lockAttempt).GetRefreshTokenByHashForUpdateNoWait(ctx, hash)
	if err != nil {
		_ = lockAttempt.Rollback(ctx) // roll back to the savepoint, un-aborting the outer tx

		if !isLockNotAvailable(err) {
			if isNoRows(err) {
				return Result{}, ErrInvalidRefreshToken
			}
			return Result{}, fmt.Errorf("auth: get refresh token: %w", err)
		}

		contended = true
		current, err = txQueries.GetRefreshTokenByHashForUpdate(ctx, hash)
		if err != nil {
			if isNoRows(err) {
				return Result{}, ErrInvalidRefreshToken
			}
			return Result{}, fmt.Errorf("auth: get refresh token (contended): %w", err)
		}
	} else if err := lockAttempt.Commit(ctx); err != nil {
		// Releases the savepoint; the row lock itself remains held by the
		// outer transaction either way.
		return Result{}, fmt.Errorf("auth: release lock-attempt savepoint: %w", err)
	}

	if current.RevokedAt.Valid {
		if contended {
			// Concurrent race loser: another request rotated this exact
			// token while we were blocked waiting on its row lock. This is
			// indistinguishable from "we simply lost a race" and must not
			// be treated as reuse.
			return Result{}, ErrInvalidRefreshToken
		}

		// No contention was observed acquiring the lock, yet the row is
		// already revoked: genuine reuse of an already-rotated token.
		if err := txQueries.RevokeRefreshTokenFamily(ctx, current.FamilyID); err != nil {
			return Result{}, fmt.Errorf("auth: revoke refresh token family on reuse: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return Result{}, fmt.Errorf("auth: commit refresh token family revocation: %w", err)
		}
		committed = true
		return Result{}, ErrRefreshTokenReuse
	}

	now := s.now()
	if current.ExpiresAt.Valid && current.ExpiresAt.Time.Before(now) {
		return Result{}, ErrInvalidRefreshToken
	}

	user, err := txQueries.GetUserByID(ctx, current.UserID)
	if err != nil {
		if isNoRows(err) {
			return Result{}, ErrInvalidRefreshToken
		}
		return Result{}, fmt.Errorf("auth: get user: %w", err)
	}

	deviceRow, err := txQueries.GetDeviceByID(ctx, storedb.GetDeviceByIDParams{ID: current.DeviceID, UserID: current.UserID})
	if err != nil {
		if isNoRows(err) {
			return Result{}, ErrInvalidRefreshToken
		}
		return Result{}, fmt.Errorf("auth: get device: %w", err)
	}

	if device != nil {
		if err := device.validate(); err != nil {
			return Result{}, err
		}
		updated, err := txQueries.UpdateDeviceMetadata(ctx, storedb.UpdateDeviceMetadataParams{
			ID:         deviceRow.ID,
			UserID:     current.UserID,
			Name:       device.Name,
			Model:      device.Model,
			OsVersion:  device.OSVersion,
			AppVersion: device.AppVersion,
		})
		if err != nil {
			return Result{}, fmt.Errorf("auth: update device metadata: %w", err)
		}
		deviceRow = updated
	}

	newRefreshToken, err := GenerateRefreshToken()
	if err != nil {
		return Result{}, err
	}
	newRow, err := txQueries.CreateRefreshToken(ctx, storedb.CreateRefreshTokenParams{
		UserID:    current.UserID,
		DeviceID:  current.DeviceID,
		TokenHash: HashRefreshToken(newRefreshToken),
		FamilyID:  current.FamilyID,
		ExpiresAt: timestamptzNow(now.Add(RefreshTokenTTL)),
	})
	if err != nil {
		return Result{}, fmt.Errorf("auth: create rotated refresh token: %w", err)
	}

	// We are holding this row's lock for the remainder of the transaction
	// (whether acquired via NOWAIT or the blocking fallback above), and we
	// have already confirmed revoked_at IS NULL under that lock, so this
	// update cannot lose a race with anyone else: no other transaction can
	// touch this row until we commit or roll back. The revoked_at IS NULL
	// guard and isNoRows handling are kept only as defense in depth.
	if _, err := txQueries.RotateRefreshToken(ctx, storedb.RotateRefreshTokenParams{
		ID:         current.ID,
		ReplacedBy: newRow.ID,
	}); err != nil {
		if isNoRows(err) {
			return Result{}, ErrInvalidRefreshToken
		}
		return Result{}, fmt.Errorf("auth: rotate refresh token: %w", err)
	}

	accessToken, err := IssueAccessToken(s.SigningKey, user.ID.String(), user.Role, deviceRow.ID.String(), now)
	if err != nil {
		return Result{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("auth: commit refresh rotation: %w", err)
	}
	committed = true

	return newResult(user, deviceRow, accessToken, newRefreshToken), nil
}

// refreshNoTx is the non-transactional fallback used when s.Pool is nil
// (unit tests substituting a fakeQuerier for Queries). fakeQuerier has no
// notion of concurrent transactions or row locking, so there is nothing for
// a transaction to buy in that setting; this preserves the CAS-style
// (compare-and-swap) rotation behavior that predates RIZ-32 M2, which is
// sufficient for sequential (non-concurrent) test scenarios.
func (s *Service) refreshNoTx(ctx context.Context, refreshToken string, device *DeviceInput) (Result, error) {
	hash := HashRefreshToken(refreshToken)
	current, err := s.Queries.GetRefreshTokenByHash(ctx, hash)
	if err != nil {
		if isNoRows(err) {
			return Result{}, ErrInvalidRefreshToken
		}
		return Result{}, fmt.Errorf("auth: get refresh token: %w", err)
	}

	if current.RevokedAt.Valid {
		// Reuse of an already-rotated (or already-revoked) token: revoke
		// the whole family per documentation/security.md's reuse-detection
		// flow.
		if err := s.Queries.RevokeRefreshTokenFamily(ctx, current.FamilyID); err != nil {
			return Result{}, fmt.Errorf("auth: revoke refresh token family on reuse: %w", err)
		}
		return Result{}, ErrRefreshTokenReuse
	}

	now := s.now()
	if current.ExpiresAt.Valid && current.ExpiresAt.Time.Before(now) {
		return Result{}, ErrInvalidRefreshToken
	}

	user, err := s.Queries.GetUserByID(ctx, current.UserID)
	if err != nil {
		if isNoRows(err) {
			return Result{}, ErrInvalidRefreshToken
		}
		return Result{}, fmt.Errorf("auth: get user: %w", err)
	}

	deviceRow, err := s.Queries.GetDeviceByID(ctx, storedb.GetDeviceByIDParams{ID: current.DeviceID, UserID: current.UserID})
	if err != nil {
		if isNoRows(err) {
			return Result{}, ErrInvalidRefreshToken
		}
		return Result{}, fmt.Errorf("auth: get device: %w", err)
	}

	if device != nil {
		if err := device.validate(); err != nil {
			return Result{}, err
		}
		updated, err := s.Queries.UpdateDeviceMetadata(ctx, storedb.UpdateDeviceMetadataParams{
			ID:         deviceRow.ID,
			UserID:     current.UserID,
			Name:       device.Name,
			Model:      device.Model,
			OsVersion:  device.OSVersion,
			AppVersion: device.AppVersion,
		})
		if err != nil {
			return Result{}, fmt.Errorf("auth: update device metadata: %w", err)
		}
		deviceRow = updated
	}

	// Create the replacement token (same family) before consuming the
	// presented one, so replaced_by can point at it.
	newRefreshToken, err := GenerateRefreshToken()
	if err != nil {
		return Result{}, err
	}
	newRow, err := s.Queries.CreateRefreshToken(ctx, storedb.CreateRefreshTokenParams{
		UserID:    current.UserID,
		DeviceID:  current.DeviceID,
		TokenHash: HashRefreshToken(newRefreshToken),
		FamilyID:  current.FamilyID,
		ExpiresAt: timestamptzNow(now.Add(RefreshTokenTTL)),
	})
	if err != nil {
		return Result{}, fmt.Errorf("auth: create rotated refresh token: %w", err)
	}

	// Atomically consume the presented token (WHERE revoked_at IS NULL). If
	// this affects no row, a concurrent request already rotated the exact
	// same token in the tiny window between our read above and this update
	// — indistinguishable from reuse from this request's point of view, so
	// treat it the same way: revoke the whole family (which also revokes
	// the dangling replacement row just created above, since it shares
	// family_id) and report reuse.
	if _, err := s.Queries.RotateRefreshToken(ctx, storedb.RotateRefreshTokenParams{
		ID:         current.ID,
		ReplacedBy: newRow.ID,
	}); err != nil {
		if isNoRows(err) {
			if revokeErr := s.Queries.RevokeRefreshTokenFamily(ctx, current.FamilyID); revokeErr != nil {
				return Result{}, fmt.Errorf("auth: revoke refresh token family on race: %w", revokeErr)
			}
			return Result{}, ErrRefreshTokenReuse
		}
		return Result{}, fmt.Errorf("auth: rotate refresh token: %w", err)
	}

	accessToken, err := IssueAccessToken(s.SigningKey, user.ID.String(), user.Role, deviceRow.ID.String(), now)
	if err != nil {
		return Result{}, err
	}

	return newResult(user, deviceRow, accessToken, newRefreshToken), nil
}

// Logout revokes the refresh token family backing the caller's session, per
// documentation/api-reference.md §Auth ("POST /v1/auth/logout: Revoke
// current refresh token"). userID is the authenticated caller's id (from the
// verified access token); refreshToken is the specific token to revoke,
// scoped to that user so one user's logout can never revoke another user's
// tokens (documentation/security.md §Tenant Isolation).
func (s *Service) Logout(ctx context.Context, userID string, refreshToken string) error {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return ErrInvalidRefreshToken
	}

	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}

	hash := HashRefreshToken(refreshToken)
	current, err := s.Queries.GetRefreshTokenByHash(ctx, hash)
	if err != nil {
		if isNoRows(err) {
			return ErrInvalidRefreshToken
		}
		return fmt.Errorf("auth: get refresh token: %w", err)
	}

	if current.UserID != uid {
		// Tenant isolation: a token that does not belong to the
		// authenticated caller is treated identically to an unknown one.
		return ErrInvalidRefreshToken
	}

	if err := s.Queries.RevokeRefreshTokenFamilyForUser(ctx, storedb.RevokeRefreshTokenFamilyForUserParams{
		FamilyID: current.FamilyID,
		UserID:   uid,
	}); err != nil {
		return fmt.Errorf("auth: revoke refresh token family: %w", err)
	}
	return nil
}
