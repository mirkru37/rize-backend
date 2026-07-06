package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// defaultListLimit / maxListLimit bound GET /v1/projects's page size, per
// documentation/api-reference.md §Conventions's cursor-based pagination
// convention, which leaves the exact numeric default/ceiling unspecified.
// This is an assumption, documented here rather than in the doc itself.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// Service implements the /v1/projects business logic.
type Service struct {
	Queries storedb.Querier
}

// Create implements POST /v1/projects.
func (s *Service) Create(ctx context.Context, userID string, req createRequest) (storedb.Project, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.Project{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return storedb.Project{}, fmt.Errorf("%w: name is required", ErrValidation)
	}
	color := strings.TrimSpace(req.Color)
	if color == "" {
		return storedb.Project{}, fmt.Errorf("%w: color is required", ErrValidation)
	}

	var id pgtype.UUID
	if req.ID != "" {
		id, err = parseUUID(req.ID)
		if err != nil {
			return storedb.Project{}, err
		}
	} else {
		id, err = newUUIDv4()
		if err != nil {
			return storedb.Project{}, fmt.Errorf("projects: %w", err)
		}
	}

	created, err := s.Queries.CreateProjectForUser(ctx, storedb.CreateProjectForUserParams{
		ID:     id,
		UserID: uid,
		Name:   name,
		Color:  color,
	})
	if err != nil {
		if code, message, ok := store.ConstraintViolation(err); ok {
			return storedb.Project{}, mapConstraintViolation(code, message)
		}
		return storedb.Project{}, fmt.Errorf("projects: create: %w", err)
	}
	return created, nil
}

// List implements GET /v1/projects.
func (s *Service) List(ctx context.Context, userID, rawCursor string, rawLimit int) ([]storedb.Project, string, bool, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	cursor, err := store.DecodeCursor(rawCursor)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: invalid cursor", ErrValidation)
	}
	limit := rawLimit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	rows, err := s.Queries.ListProjectsForUser(ctx, storedb.ListProjectsForUserParams{
		UserID:    uid,
		ServerSeq: cursor,
		Limit:     store.LimitParam(limit + 1),
	})
	if err != nil {
		return nil, "", false, fmt.Errorf("projects: list: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	nextCursor := cursor
	if len(rows) > 0 {
		nextCursor = rows[len(rows)-1].ServerSeq
	}
	return rows, store.EncodeCursor(nextCursor), hasMore, nil
}

// Get implements GET /v1/projects/{id}.
func (s *Service) Get(ctx context.Context, userID, projectID string) (storedb.Project, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.Project{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	pid, err := parseUUID(projectID)
	if err != nil {
		return storedb.Project{}, ErrNotFound
	}
	project, err := s.Queries.GetProjectForUser(ctx, storedb.GetProjectForUserParams{ID: pid, UserID: uid})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedb.Project{}, ErrNotFound
		}
		return storedb.Project{}, fmt.Errorf("projects: get: %w", err)
	}
	return project, nil
}

// Update implements PATCH /v1/projects/{id}: read-then-merge-then-write,
// matching internal/auth.UpdateProfile's pattern. updated_at is always set
// server-side to now() by UpdateProjectForUser — see that query's doc
// comment for why a direct REST edit does not go through the sync
// protocol's push-path LWW comparison.
func (s *Service) Update(ctx context.Context, userID, projectID string, req updateRequest) (storedb.Project, error) {
	current, err := s.Get(ctx, userID, projectID)
	if err != nil {
		return storedb.Project{}, err
	}

	name := current.Name
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			return storedb.Project{}, fmt.Errorf("%w: name must not be blank", ErrValidation)
		}
		name = trimmed
	}
	color := current.Color
	if req.Color != nil {
		trimmed := strings.TrimSpace(*req.Color)
		if trimmed == "" {
			return storedb.Project{}, fmt.Errorf("%w: color must not be blank", ErrValidation)
		}
		color = trimmed
	}
	// archived_at handling: the query takes an explicit value rather than
	// computing now() conditionally in SQL, per this ticket's
	// read-then-merge-then-write convention. archived=true sets it to the
	// current time; archived=false clears it; omitted leaves it unchanged.
	archivedAt := current.ArchivedAt
	if req.Archived != nil {
		if *req.Archived {
			archivedAt = nowTimestamptz()
		} else {
			archivedAt = pgtype.Timestamptz{}
		}
	}

	uid, _ := parseUUID(userID)
	pid, _ := parseUUID(projectID)

	updated, err := s.Queries.UpdateProjectForUser(ctx, storedb.UpdateProjectForUserParams{
		ID:         pid,
		UserID:     uid,
		Name:       name,
		Color:      color,
		ArchivedAt: archivedAt,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedb.Project{}, ErrNotFound
		}
		if code, message, ok := store.ConstraintViolation(err); ok {
			return storedb.Project{}, mapConstraintViolation(code, message)
		}
		return storedb.Project{}, fmt.Errorf("projects: update: %w", err)
	}
	return updated, nil
}

// Delete implements DELETE /v1/projects/{id}: soft delete via deleted_at,
// per documentation/database-schema.md's soft-delete convention, so the
// deletion propagates as a tombstone through GET /v1/sync/changes.
func (s *Service) Delete(ctx context.Context, userID, projectID string) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	pid, err := parseUUID(projectID)
	if err != nil {
		return ErrNotFound
	}
	rows, err := s.Queries.SoftDeleteProjectForUser(ctx, storedb.SoftDeleteProjectForUserParams{ID: pid, UserID: uid})
	if err != nil {
		return fmt.Errorf("projects: delete: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func mapConstraintViolation(code, message string) error {
	switch code {
	case "CONFLICT":
		return fmt.Errorf("%w: %s", ErrConflict, message)
	default:
		return fmt.Errorf("%w: %s", ErrValidation, message)
	}
}
