package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// GetProfile returns the authenticated user's profile, per
// documentation/api-reference.md §Users ("GET /v1/users/me").
func (s *Service) GetProfile(ctx context.Context, userID string) (storedb.User, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.User{}, err
	}
	user, err := s.Queries.GetUserByID(ctx, uid)
	if err != nil {
		if isNoRows(err) {
			return storedb.User{}, ErrUserNotFound
		}
		return storedb.User{}, fmt.Errorf("auth: get user: %w", err)
	}
	return user, nil
}

// ProfileUpdate carries the partial-update fields accepted by
// PATCH /v1/users/me. A nil field is left unchanged.
type ProfileUpdate struct {
	DisplayName *string
	Timezone    *string
}

// UpdateProfile applies a partial update to the authenticated user's
// profile, per documentation/api-reference.md §Users
// ("PATCH /v1/users/me: Update current user profile").
func (s *Service) UpdateProfile(ctx context.Context, userID string, update ProfileUpdate) (storedb.User, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.User{}, err
	}

	current, err := s.Queries.GetUserByID(ctx, uid)
	if err != nil {
		if isNoRows(err) {
			return storedb.User{}, ErrUserNotFound
		}
		return storedb.User{}, fmt.Errorf("auth: get user: %w", err)
	}

	displayName := current.DisplayName
	if update.DisplayName != nil {
		trimmed := strings.TrimSpace(*update.DisplayName)
		if trimmed == "" {
			return storedb.User{}, fmt.Errorf("%w: display_name must not be blank", ErrValidation)
		}
		displayName = &trimmed
	}

	timezone := current.Timezone
	if update.Timezone != nil {
		trimmed := strings.TrimSpace(*update.Timezone)
		if trimmed == "" {
			return storedb.User{}, fmt.Errorf("%w: timezone must not be blank", ErrValidation)
		}
		timezone = &trimmed
	}

	updated, err := s.Queries.UpdateUserProfile(ctx, storedb.UpdateUserProfileParams{
		ID:          uid,
		DisplayName: displayName,
		Timezone:    timezone,
	})
	if err != nil {
		if isNoRows(err) {
			return storedb.User{}, ErrUserNotFound
		}
		return storedb.User{}, fmt.Errorf("auth: update user profile: %w", err)
	}
	return updated, nil
}

// ListDevices returns every live (non-revoked) device registered to the
// authenticated user, per documentation/api-reference.md §Devices
// ("GET /v1/devices").
func (s *Service) ListDevices(ctx context.Context, userID string) ([]storedb.Device, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}
	devices, err := s.Queries.ListDevicesByUser(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("auth: list devices: %w", err)
	}
	return devices, nil
}

// RenameDevice updates a device's display name, per
// documentation/api-reference.md §Devices ("PATCH /v1/devices/{id}: Rename a
// device"). The lookup is scoped by userID per documentation/security.md
// §Tenant Isolation, so one user can never rename another user's device.
func (s *Service) RenameDevice(ctx context.Context, userID, deviceID, name string) (storedb.Device, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.Device{}, err
	}
	did, err := parseUUID(deviceID)
	if err != nil {
		return storedb.Device{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return storedb.Device{}, fmt.Errorf("%w: name is required", ErrValidation)
	}

	device, err := s.Queries.UpdateDeviceName(ctx, storedb.UpdateDeviceNameParams{
		ID:     did,
		UserID: uid,
		Name:   name,
	})
	if err != nil {
		if isNoRows(err) {
			return storedb.Device{}, ErrDeviceNotFound
		}
		return storedb.Device{}, fmt.Errorf("auth: rename device: %w", err)
	}
	return device, nil
}

// RevokeDevice revokes a device and every refresh token ever issued to it,
// per documentation/api-reference.md §Devices ("DELETE /v1/devices/{id}:
// Revoke a device and its refresh tokens").
func (s *Service) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	did, err := parseUUID(deviceID)
	if err != nil {
		return err
	}

	// Confirm the device exists and is owned by the caller before revoking,
	// so a request for an unknown/foreign device id reports 404 rather than
	// silently succeeding (RevokeDevice/RevokeRefreshTokensByDevice are
	// no-ops on a non-matching WHERE clause).
	if _, err := s.Queries.GetDeviceByID(ctx, storedb.GetDeviceByIDParams{ID: did, UserID: uid}); err != nil {
		if isNoRows(err) {
			return ErrDeviceNotFound
		}
		return fmt.Errorf("auth: get device: %w", err)
	}

	if err := s.Queries.RevokeDevice(ctx, storedb.RevokeDeviceParams{ID: did, UserID: uid}); err != nil {
		return fmt.Errorf("auth: revoke device: %w", err)
	}
	if err := s.Queries.RevokeRefreshTokensByDevice(ctx, storedb.RevokeRefreshTokensByDeviceParams{DeviceID: did, UserID: uid}); err != nil {
		return fmt.Errorf("auth: revoke device refresh tokens: %w", err)
	}
	return nil
}
