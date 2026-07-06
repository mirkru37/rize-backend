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
func (s *Service) Refresh(ctx context.Context, refreshToken string, device *DeviceInput) (Result, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return Result{}, ErrInvalidRefreshToken
	}

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
