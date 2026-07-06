package reports

import (
	"time"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

const (
	timeLayout = time.RFC3339
	dateLayout = "2006-01-02"
)

type categoryBreakdownDTO struct {
	Category string `json:"category"`
	Seconds  int64  `json:"seconds"`
}

func toCategoryBreakdown(buckets []*bucket) []categoryBreakdownDTO {
	out := make([]categoryBreakdownDTO, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, categoryBreakdownDTO{Category: b.Name, Seconds: b.Seconds})
	}
	return out
}

type appBreakdownDTO struct {
	App      string `json:"app"`
	BundleID string `json:"bundle_id,omitempty"`
	Seconds  int64  `json:"seconds"`
}

func toAppBreakdown(buckets []*bucket) []appBreakdownDTO {
	out := make([]appBreakdownDTO, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, appBreakdownDTO{App: b.Name, BundleID: b.BundleID, Seconds: b.Seconds})
	}
	return out
}

type projectBreakdownDTO struct {
	Project string `json:"project"`
	Seconds int64  `json:"seconds"`
}

func toProjectBreakdown(buckets []*bucket) []projectBreakdownDTO {
	out := make([]projectBreakdownDTO, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, projectBreakdownDTO{Project: b.Name, Seconds: b.Seconds})
	}
	return out
}

// summaryDTO is GET /v1/reports/summary's response body.
type summaryDTO struct {
	From                string                 `json:"from"`
	To                  string                 `json:"to"`
	TotalTrackedSeconds int64                  `json:"total_tracked_seconds"`
	Categories          []categoryBreakdownDTO `json:"categories"`
}

// dailyDTO is GET /v1/reports/daily's response body, matching
// documentation/api-reference.md's worked example exactly.
type dailyDTO struct {
	Date                string                 `json:"date"`
	TotalTrackedSeconds int64                  `json:"total_tracked_seconds"`
	Categories          []categoryBreakdownDTO `json:"categories"`
}

// categoriesDTO is GET /v1/reports/categories' response body.
type categoriesDTO struct {
	From       string                 `json:"from"`
	To         string                 `json:"to"`
	Categories []categoryBreakdownDTO `json:"categories"`
}

// appsDTO is GET /v1/reports/apps' response body.
type appsDTO struct {
	From string            `json:"from"`
	To   string            `json:"to"`
	Apps []appBreakdownDTO `json:"apps"`
}

// projectsDTO is GET /v1/reports/projects' response body.
type projectsDTO struct {
	From     string                `json:"from"`
	To       string                `json:"to"`
	Projects []projectBreakdownDTO `json:"projects"`
}

// timelineEventDTO is one item of GET /v1/reports/timeline's data array,
// matching internal/activities' eventDTO shape (see reports/timeline.go's
// doc comment for why this endpoint returns raw, untrimmed events).
type timelineEventDTO struct {
	EventID    string  `json:"event_id"`
	DeviceID   string  `json:"device_id"`
	StartedAt  string  `json:"started_at"`
	EndedAt    string  `json:"ended_at"`
	DurationS  int32   `json:"duration_s"`
	Type       string  `json:"type"`
	Source     string  `json:"source"`
	Precision  string  `json:"precision"`
	AppID      *string `json:"app_id,omitempty"`
	CategoryID *string `json:"category_id,omitempty"`
	ProjectID  *string `json:"project_id,omitempty"`
}

func toTimelineEventDTO(e storedb.ActivityEvent) timelineEventDTO {
	dto := timelineEventDTO{
		EventID:   e.EventID.String(),
		DeviceID:  e.DeviceID.String(),
		Type:      e.Type,
		Source:    e.Source,
		Precision: e.Precision,
	}
	if e.DurationS != nil {
		dto.DurationS = *e.DurationS
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

// timelineResponse is GET /v1/reports/timeline's response body, per
// documentation/api-reference.md §Conventions' cursor-pagination envelope.
type timelineResponse struct {
	Data       []timelineEventDTO `json:"data"`
	NextCursor string             `json:"next_cursor"`
	HasMore    bool               `json:"has_more"`
}
