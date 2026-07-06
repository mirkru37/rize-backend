package focussessions

import (
	"time"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

const timeLayout = time.RFC3339

type focusSessionDTO struct {
	ID               string  `json:"id"`
	DeviceID         string  `json:"device_id"`
	ProjectID        *string `json:"project_id,omitempty"`
	Kind             string  `json:"kind"`
	PlannedDurationS *int32  `json:"planned_duration_s,omitempty"`
	StartedAt        string  `json:"started_at"`
	EndedAt          *string `json:"ended_at,omitempty"`
	Status           string  `json:"status"`
	Note             *string `json:"note,omitempty"`
	UpdatedAt        string  `json:"updated_at"`
	ServerSeq        int64   `json:"server_seq"`
}

func toDTO(fs storedb.FocusSession) focusSessionDTO {
	dto := focusSessionDTO{
		ID:               fs.ID.String(),
		DeviceID:         fs.DeviceID.String(),
		Kind:             fs.Kind,
		PlannedDurationS: fs.PlannedDurationS,
		Status:           fs.Status,
		Note:             fs.Note,
		ServerSeq:        fs.ServerSeq,
	}
	if fs.ProjectID.Valid {
		s := fs.ProjectID.String()
		dto.ProjectID = &s
	}
	if fs.StartedAt.Valid {
		dto.StartedAt = fs.StartedAt.Time.UTC().Format(timeLayout)
	}
	if fs.EndedAt.Valid {
		s := fs.EndedAt.Time.UTC().Format(timeLayout)
		dto.EndedAt = &s
	}
	if fs.UpdatedAt.Valid {
		dto.UpdatedAt = fs.UpdatedAt.Time.UTC().Format(timeLayout)
	}
	return dto
}

// createRequest is the POST /v1/focus-sessions request body. id is
// optional (see uuid.go's newUUIDv4). device_id is required —
// focus_sessions.device_id is NOT NULL per
// documentation/database-schema.md.
type createRequest struct {
	ID               string  `json:"id,omitempty"`
	DeviceID         string  `json:"device_id"`
	ProjectID        string  `json:"project_id,omitempty"`
	Kind             string  `json:"kind"`
	PlannedDurationS *int32  `json:"planned_duration_s"`
	StartedAt        string  `json:"started_at"`
	EndedAt          string  `json:"ended_at,omitempty"`
	Status           string  `json:"status"`
	Note             *string `json:"note"`
}

// updateRequest is the PATCH /v1/focus-sessions/{id} request body. Every
// field is optional; a nil/omitted field leaves the current value
// unchanged. ProjectID/EndedAt use a pointer-to-pointer-like convention
// via the Clear* flags so a caller can explicitly null out an optional
// field rather than merely never touching it.
type updateRequest struct {
	DeviceID         *string `json:"device_id"`
	ProjectID        *string `json:"project_id"`
	ClearProjectID   bool    `json:"clear_project_id"`
	Kind             *string `json:"kind"`
	PlannedDurationS *int32  `json:"planned_duration_s"`
	StartedAt        *string `json:"started_at"`
	EndedAt          *string `json:"ended_at"`
	ClearEndedAt     bool    `json:"clear_ended_at"`
	Status           *string `json:"status"`
	Note             *string `json:"note"`
}

// listResponse: see internal/projects/dto.go's listResponse doc comment —
// same chosen envelope, same open-question caveat.
type listResponse struct {
	Data       []focusSessionDTO `json:"data"`
	NextCursor string            `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}
