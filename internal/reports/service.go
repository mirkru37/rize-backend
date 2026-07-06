package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

var validPrecision = map[string]bool{"exact": true, "approximate": true}

// Service implements the /v1/reports/* and (see timeline.go)
// /v1/activities-shaped /v1/reports/timeline business logic.
type Service struct {
	Queries storedb.Querier
}

// reportFilters is the common filter set every /v1/reports/* endpoint
// (except /daily, which is single-category-breakdown-shaped but takes the
// same filters) accepts, mirroring GET /v1/activities' filters per
// documentation/api-reference.md §Activities & reports.
type reportFilters struct {
	AppID      string
	CategoryID string
	ProjectID  string
	DeviceID   string
	Precision  string
}

func (f reportFilters) validatePrecision() error {
	if f.Precision != "" && !validPrecision[f.Precision] {
		return fmt.Errorf("%w: precision must be one of exact, approximate", ErrValidation)
	}
	return nil
}

// CategoriesResult is GET /v1/reports/categories' data.
type CategoriesResult struct {
	From       time.Time
	To         time.Time
	Categories []*bucket
}

// AppsResult is GET /v1/reports/apps' data.
type AppsResult struct {
	From time.Time
	To   time.Time
	Apps []*bucket
}

// ProjectsResult is GET /v1/reports/projects' data.
type ProjectsResult struct {
	From     time.Time
	To       time.Time
	Projects []*bucket
}

// SummaryResult is GET /v1/reports/summary's data.
type SummaryResult struct {
	From                time.Time
	To                  time.Time
	TotalTrackedSeconds int64
	Categories          []*bucket
}

// DailyResult is GET /v1/reports/daily's data, matching
// documentation/api-reference.md's worked example shape.
type DailyResult struct {
	Date                time.Time
	TotalTrackedSeconds int64
	Categories          []*bucket
}

func sumSeconds(m map[string]*bucket) int64 {
	var total int64
	for _, b := range m {
		total += b.Seconds
	}
	return total
}

func toSortedSlice(m map[string]*bucket) []*bucket {
	out := make([]*bucket, 0, len(m))
	for _, b := range m {
		out = append(out, b)
	}
	sortBucketsBySecondsDesc(out)
	return out
}

func validateRange(from, to time.Time) error {
	if from.IsZero() || to.IsZero() {
		return fmt.Errorf("%w: from and to are required", ErrValidation)
	}
	if err := store.ValidateRange(from, to); err != nil {
		return fmt.Errorf("%w: %s", ErrValidation, err.Error())
	}
	return nil
}

// Categories implements GET /v1/reports/categories.
func (s *Service) Categories(ctx context.Context, userID string, from, to time.Time, f reportFilters) (CategoriesResult, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return CategoriesResult{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	if err := validateRange(from, to); err != nil {
		return CategoriesResult{}, err
	}
	if err := f.validatePrecision(); err != nil {
		return CategoriesResult{}, err
	}
	totals, err := s.categoryTotals(ctx, uid, from, to, f)
	if err != nil {
		return CategoriesResult{}, err
	}
	return CategoriesResult{From: from, To: to, Categories: toSortedSlice(totals)}, nil
}

// Apps implements GET /v1/reports/apps.
func (s *Service) Apps(ctx context.Context, userID string, from, to time.Time, f reportFilters) (AppsResult, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return AppsResult{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	if err := validateRange(from, to); err != nil {
		return AppsResult{}, err
	}
	if err := f.validatePrecision(); err != nil {
		return AppsResult{}, err
	}
	totals, err := s.appTotals(ctx, uid, from, to, f)
	if err != nil {
		return AppsResult{}, err
	}
	return AppsResult{From: from, To: to, Apps: toSortedSlice(totals)}, nil
}

// Projects implements GET /v1/reports/projects.
func (s *Service) Projects(ctx context.Context, userID string, from, to time.Time, f reportFilters) (ProjectsResult, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return ProjectsResult{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	if err := validateRange(from, to); err != nil {
		return ProjectsResult{}, err
	}
	if err := f.validatePrecision(); err != nil {
		return ProjectsResult{}, err
	}
	totals, err := s.projectTotals(ctx, uid, from, to, f)
	if err != nil {
		return ProjectsResult{}, err
	}
	return ProjectsResult{From: from, To: to, Projects: toSortedSlice(totals)}, nil
}

// Summary implements GET /v1/reports/summary: the same overlap-trimmed
// category breakdown as Categories, plus the grand total across every
// category (including "Uncategorized"), per this package's doc comment.
func (s *Service) Summary(ctx context.Context, userID string, from, to time.Time, f reportFilters) (SummaryResult, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return SummaryResult{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	if err := validateRange(from, to); err != nil {
		return SummaryResult{}, err
	}
	if err := f.validatePrecision(); err != nil {
		return SummaryResult{}, err
	}
	totals, err := s.categoryTotals(ctx, uid, from, to, f)
	if err != nil {
		return SummaryResult{}, err
	}
	return SummaryResult{From: from, To: to, TotalTrackedSeconds: sumSeconds(totals), Categories: toSortedSlice(totals)}, nil
}

// Daily implements GET /v1/reports/daily?date=YYYY-MM-DD, per
// documentation/api-reference.md's worked example: a single UTC calendar
// day's category breakdown plus its total.
func (s *Service) Daily(ctx context.Context, userID string, date time.Time, f reportFilters) (DailyResult, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return DailyResult{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	if err := f.validatePrecision(); err != nil {
		return DailyResult{}, err
	}
	from := dayStart(date)
	to := from.AddDate(0, 0, 1)
	totals, err := s.categoryTotals(ctx, uid, from, to, f)
	if err != nil {
		return DailyResult{}, err
	}
	return DailyResult{Date: from, TotalTrackedSeconds: sumSeconds(totals), Categories: toSortedSlice(totals)}, nil
}
