package categories

import (
	"time"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

type categoryDTO struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Color        string `json:"color"`
	Productivity int16  `json:"productivity"`
	// System is true when this category is a system default
	// (user_id IS NULL per documentation/database-schema.md), i.e. it is
	// read-only via this API — see doc.go.
	System    bool   `json:"system"`
	UpdatedAt string `json:"updated_at"`
	ServerSeq int64  `json:"server_seq"`
}

func toDTO(c storedb.Category) categoryDTO {
	dto := categoryDTO{
		ID:           c.ID.String(),
		Name:         c.Name,
		Color:        c.Color,
		Productivity: c.Productivity,
		System:       !c.UserID.Valid,
		ServerSeq:    c.ServerSeq,
	}
	if c.UpdatedAt.Valid {
		dto.UpdatedAt = c.UpdatedAt.Time.UTC().Format(time.RFC3339)
	}
	return dto
}

type createRequest struct {
	Name         string `json:"name"`
	Color        string `json:"color"`
	Productivity int16  `json:"productivity"`
}

type updateRequest struct {
	Name         *string `json:"name"`
	Color        *string `json:"color"`
	Productivity *int16  `json:"productivity"`
}

// listResponse: see internal/projects/dto.go's listResponse doc comment —
// same chosen envelope, same open-question caveat.
type listResponse struct {
	Data       []categoryDTO `json:"data"`
	NextCursor string        `json:"next_cursor"`
	HasMore    bool          `json:"has_more"`
}
