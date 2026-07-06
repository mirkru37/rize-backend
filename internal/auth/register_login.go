package auth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// Register creates a new email/password account, registers the presented
// device, and issues an initial access/refresh token pair — per
// documentation/api-reference.md §Auth ("POST /v1/auth/register: Email +
// password signup") and documentation/security.md §Authentication design
// (argon2id password hashing).
func (s *Service) Register(ctx context.Context, email, password string, device DeviceInput) (Result, error) {
	normalizedEmail, err := validEmail(email)
	if err != nil {
		return Result{}, err
	}
	if err := validPassword(password); err != nil {
		return Result{}, err
	}
	if err := device.validate(); err != nil {
		return Result{}, err
	}

	hash, err := HashPassword(password)
	if err != nil {
		return Result{}, fmt.Errorf("auth: hash password: %w", err)
	}

	user, err := s.Queries.CreateUser(ctx, storedb.CreateUserParams{
		Email:        strPtr(normalizedEmail),
		PasswordHash: strPtr(hash),
		Role:         "user",
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Result{}, ErrEmailTaken
		}
		return Result{}, fmt.Errorf("auth: create user: %w", err)
	}

	deviceRow, err := s.Queries.CreateDevice(ctx, storedb.CreateDeviceParams{
		UserID:     user.ID,
		Platform:   device.Platform,
		Name:       device.Name,
		Model:      device.Model,
		OsVersion:  device.OSVersion,
		AppVersion: device.AppVersion,
	})
	if err != nil {
		return Result{}, fmt.Errorf("auth: create device: %w", err)
	}

	access, refresh, err := s.issueTokenPair(ctx, user, deviceRow)
	if err != nil {
		return Result{}, err
	}

	return newResult(user, deviceRow, access, refresh), nil
}

// Login authenticates an email/password account, resolves (creating or
// updating) the presented device, and issues a new access/refresh token
// pair — per documentation/api-reference.md §Auth ("POST /v1/auth/login")
// and its worked example.
func (s *Service) Login(ctx context.Context, email, password string, device DeviceInput) (Result, error) {
	normalizedEmail, err := validEmail(email)
	if err != nil {
		return Result{}, err
	}
	if password == "" {
		return Result{}, ErrInvalidCredentials
	}
	if err := device.validate(); err != nil {
		return Result{}, err
	}

	user, err := s.Queries.GetUserByEmail(ctx, strPtr(normalizedEmail))
	if err != nil {
		if isNoRows(err) {
			return Result{}, ErrInvalidCredentials
		}
		return Result{}, fmt.Errorf("auth: get user by email: %w", err)
	}

	if user.PasswordHash == nil {
		// Apple-only account: no password to check against.
		return Result{}, ErrInvalidCredentials
	}

	ok, err := VerifyPassword(*user.PasswordHash, password)
	if err != nil {
		return Result{}, fmt.Errorf("auth: verify password: %w", err)
	}
	if !ok {
		return Result{}, ErrInvalidCredentials
	}

	deviceRow, err := s.resolveDevice(ctx, user.ID, device)
	if err != nil {
		return Result{}, err
	}

	access, refresh, err := s.issueTokenPair(ctx, user, deviceRow)
	if err != nil {
		return Result{}, err
	}

	return newResult(user, deviceRow, access, refresh), nil
}

// resolveDevice creates a new device row, or — if device.ID names a live
// device already owned by userID — updates that device's self-reported
// metadata in place, per documentation/security.md §Token model ("a device
// row is created/updated and bound to the refresh token").
func (s *Service) resolveDevice(ctx context.Context, userID pgtype.UUID, device DeviceInput) (storedb.Device, error) {
	if device.ID != "" {
		id, err := parseUUID(device.ID)
		if err == nil {
			updated, err := s.Queries.UpdateDeviceMetadata(ctx, storedb.UpdateDeviceMetadataParams{
				ID:         id,
				UserID:     userID,
				Name:       device.Name,
				Model:      device.Model,
				OsVersion:  device.OSVersion,
				AppVersion: device.AppVersion,
			})
			if err == nil {
				return updated, nil
			}
			if !isNoRows(err) {
				return storedb.Device{}, fmt.Errorf("auth: update device metadata: %w", err)
			}
			// Fall through to create a new device: the supplied id did not
			// resolve to a live device owned by this user (unknown,
			// revoked, or belonging to someone else).
		}
	}

	created, err := s.Queries.CreateDevice(ctx, storedb.CreateDeviceParams{
		UserID:     userID,
		Platform:   device.Platform,
		Name:       device.Name,
		Model:      device.Model,
		OsVersion:  device.OSVersion,
		AppVersion: device.AppVersion,
	})
	if err != nil {
		return storedb.Device{}, fmt.Errorf("auth: create device: %w", err)
	}
	return created, nil
}

// issueTokenPair mints a fresh refresh token (a new rotation family) and a
// matching access token for user/device, persisting the refresh token
// hashed at rest per documentation/security.md §Token model.
func (s *Service) issueTokenPair(ctx context.Context, user storedb.User, device storedb.Device) (accessToken, refreshToken string, err error) {
	familyID, err := newUUIDv4()
	if err != nil {
		return "", "", err
	}

	refreshToken, err = GenerateRefreshToken()
	if err != nil {
		return "", "", err
	}

	now := s.now()
	_, err = s.Queries.CreateRefreshToken(ctx, storedb.CreateRefreshTokenParams{
		UserID:    user.ID,
		DeviceID:  device.ID,
		TokenHash: HashRefreshToken(refreshToken),
		FamilyID:  familyID,
		ExpiresAt: timestamptzNow(now.Add(RefreshTokenTTL)),
	})
	if err != nil {
		return "", "", fmt.Errorf("auth: create refresh token: %w", err)
	}

	accessToken, err = IssueAccessToken(s.SigningKey, user.ID.String(), user.Role, device.ID.String(), now)
	if err != nil {
		return "", "", err
	}

	return accessToken, refreshToken, nil
}
