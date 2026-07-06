package tags

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

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// Service implements the /v1/tags business logic.
type Service struct {
	Queries storedb.Querier
}

// Create implements POST /v1/tags.
func (s *Service) Create(ctx context.Context, userID string, req createRequest) (storedb.Tag, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.Tag{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return storedb.Tag{}, fmt.Errorf("%w: name is required", ErrValidation)
	}

	var id pgtype.UUID
	if req.ID != "" {
		id, err = parseUUID(req.ID)
		if err != nil {
			return storedb.Tag{}, err
		}
	} else {
		id, err = newUUIDv4()
		if err != nil {
			return storedb.Tag{}, fmt.Errorf("tags: %w", err)
		}
	}

	created, err := s.Queries.CreateTagForUser(ctx, storedb.CreateTagForUserParams{ID: id, UserID: uid, Name: name})
	if err != nil {
		if code, message, ok := store.ConstraintViolation(err); ok {
			return storedb.Tag{}, mapConstraintViolation(code, message)
		}
		return storedb.Tag{}, fmt.Errorf("tags: create: %w", err)
	}
	return created, nil
}

// List implements GET /v1/tags.
func (s *Service) List(ctx context.Context, userID, rawCursor string, rawLimit int) ([]storedb.Tag, string, bool, error) {
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

	rows, err := s.Queries.ListTagsForUser(ctx, storedb.ListTagsForUserParams{UserID: uid, ServerSeq: cursor, Limit: store.LimitParam(limit + 1)})
	if err != nil {
		return nil, "", false, fmt.Errorf("tags: list: %w", err)
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

// Get implements GET /v1/tags/{id}.
func (s *Service) Get(ctx context.Context, userID, tagID string) (storedb.Tag, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.Tag{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	tid, err := parseUUID(tagID)
	if err != nil {
		return storedb.Tag{}, ErrNotFound
	}
	tag, err := s.Queries.GetTagForUser(ctx, storedb.GetTagForUserParams{ID: tid, UserID: uid})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedb.Tag{}, ErrNotFound
		}
		return storedb.Tag{}, fmt.Errorf("tags: get: %w", err)
	}
	return tag, nil
}

// Update implements PATCH /v1/tags/{id}.
func (s *Service) Update(ctx context.Context, userID, tagID string, req updateRequest) (storedb.Tag, error) {
	current, err := s.Get(ctx, userID, tagID)
	if err != nil {
		return storedb.Tag{}, err
	}
	name := current.Name
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			return storedb.Tag{}, fmt.Errorf("%w: name must not be blank", ErrValidation)
		}
		name = trimmed
	}

	uid, _ := parseUUID(userID)
	tid, _ := parseUUID(tagID)

	updated, err := s.Queries.UpdateTagForUser(ctx, storedb.UpdateTagForUserParams{ID: tid, UserID: uid, Name: name})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedb.Tag{}, ErrNotFound
		}
		if code, message, ok := store.ConstraintViolation(err); ok {
			return storedb.Tag{}, mapConstraintViolation(code, message)
		}
		return storedb.Tag{}, fmt.Errorf("tags: update: %w", err)
	}
	return updated, nil
}

// Delete implements DELETE /v1/tags/{id}.
func (s *Service) Delete(ctx context.Context, userID, tagID string) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	tid, err := parseUUID(tagID)
	if err != nil {
		return ErrNotFound
	}
	rows, err := s.Queries.SoftDeleteTagForUser(ctx, storedb.SoftDeleteTagForUserParams{ID: tid, UserID: uid})
	if err != nil {
		return fmt.Errorf("tags: delete: %w", err)
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
