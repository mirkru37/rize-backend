package projects

import (
	"time"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// projectDTO is the wire shape of a project in every response
// (create/get/list/update), per documentation/database-schema.md's
// projects table.
type projectDTO struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Color      string  `json:"color"`
	ArchivedAt *string `json:"archived_at,omitempty"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	ServerSeq  int64   `json:"server_seq"`
}

func toDTO(p storedb.Project) projectDTO {
	dto := projectDTO{
		ID:        p.ID.String(),
		Name:      p.Name,
		Color:     p.Color,
		ServerSeq: p.ServerSeq,
	}
	if p.CreatedAt.Valid {
		dto.CreatedAt = p.CreatedAt.Time.UTC().Format(time.RFC3339)
	}
	if p.UpdatedAt.Valid {
		dto.UpdatedAt = p.UpdatedAt.Time.UTC().Format(time.RFC3339)
	}
	if p.ArchivedAt.Valid {
		s := p.ArchivedAt.Time.UTC().Format(time.RFC3339)
		dto.ArchivedAt = &s
	}
	return dto
}

// createRequest is the POST /v1/projects request body. id is optional; see
// uuid.go's newUUIDv4 doc comment.
type createRequest struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// updateRequest is the PATCH /v1/projects/{id} request body. Every field
// is optional; a nil field leaves the current value unchanged. archived is
// a bool rather than a timestamp: true sets archived_at = now(), false
// clears it, per documentation/database-schema.md's projects.archived_at
// ("archiving hides a project from active selection lists").
type updateRequest struct {
	Name     *string `json:"name"`
	Color    *string `json:"color"`
	Archived *bool   `json:"archived"`
}

// listResponse is the envelope for GET /v1/projects, per
// documentation/api-reference.md §Conventions's cursor-based pagination
// convention. The exact envelope shape is left an open question by that
// doc; {"data": [...], "next_cursor": ..., "has_more": ...} is this
// package's chosen interpretation, consistent across every CRUD list
// endpoint added in this ticket.
type listResponse struct {
	Data       []projectDTO `json:"data"`
	NextCursor string       `json:"next_cursor"`
	HasMore    bool         `json:"has_more"`
}
