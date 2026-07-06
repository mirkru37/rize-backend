package activities

import (
	"time"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

const timeLayout = time.RFC3339

// eventDTO is one item of GET /v1/activities' data array: a raw
// activity_events row, per documentation/database-schema.md's
// activity_events table.
type eventDTO struct {
	EventID     string  `json:"event_id"`
	DeviceID    string  `json:"device_id"`
	StartedAt   string  `json:"started_at"`
	EndedAt     string  `json:"ended_at"`
	DurationS   int32   `json:"duration_s"`
	Type        string  `json:"type"`
	Source      string  `json:"source"`
	Precision   string  `json:"precision"`
	AppID       *string `json:"app_id,omitempty"`
	RawBundleID *string `json:"raw_bundle_id,omitempty"`
	WindowTitle *string `json:"window_title,omitempty"`
	URL         *string `json:"url,omitempty"`
	CategoryID  *string `json:"category_id,omitempty"`
	ProjectID   *string `json:"project_id,omitempty"`
}

func toDTO(e storedb.ActivityEvent) eventDTO {
	dto := eventDTO{
		EventID:     e.EventID.String(),
		DeviceID:    e.DeviceID.String(),
		DurationS:   valueOrZero(e.DurationS),
		Type:        e.Type,
		Source:      e.Source,
		Precision:   e.Precision,
		RawBundleID: e.RawBundleID,
		WindowTitle: e.WindowTitle,
		URL:         e.Url,
	}
	if e.StartedAt.Valid {
		dto.StartedAt = e.StartedAt.Time.UTC().Format(timeLayout)
	}
	if e.EndedAt.Valid {
		dto.EndedAt = e.EndedAt.Time.UTC().Format(timeLayout)
	}
	if e.AppID.Valid {
		s := e.AppID.String()
		dto.AppID = &s
	}
	if e.CategoryID.Valid {
		s := e.CategoryID.String()
		dto.CategoryID = &s
	}
	if e.ProjectID.Valid {
		s := e.ProjectID.String()
		dto.ProjectID = &s
	}
	return dto
}

func valueOrZero(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

// listResponse is GET /v1/activities' response body, per
// documentation/api-reference.md §Conventions' cursor-pagination envelope.
type listResponse struct {
	Data       []eventDTO `json:"data"`
	NextCursor string     `json:"next_cursor"`
	HasMore    bool       `json:"has_more"`
}
