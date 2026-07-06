package categories

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// Service implements the /v1/categories business logic.
type Service struct {
	Queries storedb.Querier
}

func validateProductivity(p int16) error {
	if p < -2 || p > 2 {
		return fmt.Errorf("%w: productivity must be between -2 and 2", ErrValidation)
	}
	return nil
}

// Create implements POST /v1/categories.
func (s *Service) Create(ctx context.Context, userID string, req createRequest) (storedb.Category, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.Category{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return storedb.Category{}, fmt.Errorf("%w: name is required", ErrValidation)
	}
	color := strings.TrimSpace(req.Color)
	if color == "" {
		return storedb.Category{}, fmt.Errorf("%w: color is required", ErrValidation)
	}
	if err := validateProductivity(req.Productivity); err != nil {
		return storedb.Category{}, err
	}

	created, err := s.Queries.CreateCategoryForUser(ctx, storedb.CreateCategoryForUserParams{
		UserID:       uid,
		Name:         name,
		Color:        color,
		Productivity: req.Productivity,
	})
	if err != nil {
		if _, message, ok := store.ConstraintViolation(err); ok {
			return storedb.Category{}, fmt.Errorf("%w: %s", ErrValidation, message)
		}
		return storedb.Category{}, fmt.Errorf("categories: create: %w", err)
	}
	return created, nil
}

// List implements GET /v1/categories.
func (s *Service) List(ctx context.Context, userID, rawCursor string, rawLimit int) ([]storedb.Category, string, bool, error) {
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

	rows, err := s.Queries.ListCategoriesForUser(ctx, storedb.ListCategoriesForUserParams{
		UserID:    uid,
		ServerSeq: cursor,
		Limit:     store.LimitParam(limit + 1),
	})
	if err != nil {
		return nil, "", false, fmt.Errorf("categories: list: %w", err)
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

// Get returns a category readable by userID: a system default or one the
// caller owns, per doc.go.
func (s *Service) Get(ctx context.Context, userID, categoryID string) (storedb.Category, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.Category{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	cid, err := parseUUID(categoryID)
	if err != nil {
		return storedb.Category{}, ErrNotFound
	}
	category, err := s.Queries.GetCategoryForUser(ctx, storedb.GetCategoryForUserParams{ID: cid, UserID: uid})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedb.Category{}, ErrNotFound
		}
		return storedb.Category{}, fmt.Errorf("categories: get: %w", err)
	}
	return category, nil
}

// Update implements PATCH /v1/categories/{id}.
func (s *Service) Update(ctx context.Context, userID, categoryID string, req updateRequest) (storedb.Category, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.Category{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	cid, err := parseUUID(categoryID)
	if err != nil {
		return storedb.Category{}, ErrNotFound
	}

	current, err := s.Queries.GetOwnCategoryForUser(ctx, storedb.GetOwnCategoryForUserParams{ID: cid, UserID: uid})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedb.Category{}, ErrNotFound
		}
		return storedb.Category{}, fmt.Errorf("categories: get own: %w", err)
	}

	name := current.Name
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			return storedb.Category{}, fmt.Errorf("%w: name must not be blank", ErrValidation)
		}
		name = trimmed
	}
	color := current.Color
	if req.Color != nil {
		trimmed := strings.TrimSpace(*req.Color)
		if trimmed == "" {
			return storedb.Category{}, fmt.Errorf("%w: color must not be blank", ErrValidation)
		}
		color = trimmed
	}
	productivity := current.Productivity
	if req.Productivity != nil {
		if err := validateProductivity(*req.Productivity); err != nil {
			return storedb.Category{}, err
		}
		productivity = *req.Productivity
	}

	updated, err := s.Queries.UpdateOwnCategoryForUser(ctx, storedb.UpdateOwnCategoryForUserParams{
		ID:           cid,
		UserID:       uid,
		Name:         name,
		Color:        color,
		Productivity: productivity,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedb.Category{}, ErrNotFound
		}
		if _, message, ok := store.ConstraintViolation(err); ok {
			return storedb.Category{}, fmt.Errorf("%w: %s", ErrValidation, message)
		}
		return storedb.Category{}, fmt.Errorf("categories: update: %w", err)
	}
	return updated, nil
}

// Delete implements DELETE /v1/categories/{id}.
func (s *Service) Delete(ctx context.Context, userID, categoryID string) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	cid, err := parseUUID(categoryID)
	if err != nil {
		return ErrNotFound
	}
	rows, err := s.Queries.SoftDeleteOwnCategoryForUser(ctx, storedb.SoftDeleteOwnCategoryForUserParams{ID: cid, UserID: uid})
	if err != nil {
		return fmt.Errorf("categories: delete: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}
