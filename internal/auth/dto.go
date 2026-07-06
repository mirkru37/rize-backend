package auth

import (
	"time"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// deviceDTO is the wire shape of device metadata, both accepted in
// request bodies (register/login/refresh) and returned in responses.
type deviceDTO struct {
	ID         string `json:"id,omitempty"`
	Platform   string `json:"platform"`
	Name       string `json:"name"`
	Model      string `json:"model"`
	OSVersion  string `json:"os_version"`
	AppVersion string `json:"app_version"`
}

func (d deviceDTO) toInput() DeviceInput {
	return DeviceInput(d)
}

func deviceToDTO(d storedb.Device) deviceDTO {
	return deviceDTO{
		ID:         d.ID.String(),
		Platform:   d.Platform,
		Name:       d.Name,
		Model:      d.Model,
		OSVersion:  d.OsVersion,
		AppVersion: d.AppVersion,
	}
}

// userDTO is the wire shape of a user profile, per
// documentation/api-reference.md's worked login example
// ({"id": ..., "email": ...}) extended with the profile fields
// GET/PATCH /v1/users/me expose.
type userDTO struct {
	ID          string  `json:"id"`
	Email       *string `json:"email,omitempty"`
	DisplayName *string `json:"display_name,omitempty"`
	Timezone    *string `json:"timezone,omitempty"`
	Role        string  `json:"role"`
	CreatedAt   string  `json:"created_at,omitempty"`
}

func userToDTO(u storedb.User) userDTO {
	dto := userDTO{
		ID:          u.ID.String(),
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Timezone:    u.Timezone,
		Role:        u.Role,
	}
	if u.CreatedAt.Valid {
		dto.CreatedAt = u.CreatedAt.Time.UTC().Format(time.RFC3339)
	}
	return dto
}

// authResponse is the response body for register/login/refresh, per
// documentation/api-reference.md's worked POST /v1/auth/login example.
type authResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int64     `json:"expires_in"`
	User         userDTO   `json:"user"`
	Device       deviceDTO `json:"device"`
}

func toAuthResponse(result Result) authResponse {
	return authResponse{
		AccessToken:  result.Tokens.AccessToken,
		RefreshToken: result.Tokens.RefreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    result.Tokens.AccessExpiresIn,
		User:         userToDTO(result.User),
		Device:       deviceToDTO(result.Device),
	}
}
