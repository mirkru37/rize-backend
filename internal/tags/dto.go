package tags

import (
	"time"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

type tagDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UpdatedAt string `json:"updated_at"`
	ServerSeq int64  `json:"server_seq"`
}

func toDTO(t storedb.Tag) tagDTO {
	dto := tagDTO{ID: t.ID.String(), Name: t.Name, ServerSeq: t.ServerSeq}
	if t.UpdatedAt.Valid {
		dto.UpdatedAt = t.UpdatedAt.Time.UTC().Format(time.RFC3339)
	}
	return dto
}

type createRequest struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

type updateRequest struct {
	Name *string `json:"name"`
}

// listResponse: see internal/projects/dto.go's listResponse doc comment —
// same chosen envelope, same open-question caveat.
type listResponse struct {
	Data       []tagDTO `json:"data"`
	NextCursor string   `json:"next_cursor"`
	HasMore    bool     `json:"has_more"`
}
