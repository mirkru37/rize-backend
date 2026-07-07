package auth_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// errNotImplementedByFakeQuerier is returned by the internal/sync-only
// Querier methods stubbed out below; no internal/auth test exercises them.
var errNotImplementedByFakeQuerier = errors.New("fakeQuerier: not implemented (internal/sync method)")

// fakeQuerier is a minimal, in-memory implementation of storedb.Querier
// used to unit-test internal/auth's Service and HTTP handlers without a
// real database. It intentionally re-implements the constraints the real
// schema enforces that the service layer depends on (unique email,
// tenant-scoped lookups, CAS-style rotation) so tests exercise the same
// behavior the Postgres-backed implementation provides.
type fakeQuerier struct {
	mu            sync.Mutex
	users         map[string]storedb.User // key: id string
	usersByEmail  map[string]string       // key: normalized email -> id string
	devices       map[string]storedb.Device
	refreshTokens map[string]storedb.RefreshToken // key: id string
	tokensByHash  map[string]string               // key: hex(hash) -> id string
	seq           int64

	// failOnce, keyed by method name, lets a test force the next call to
	// that method to fail with the given error — a one-shot fault
	// injection hook used to exercise Service methods' "unexpected
	// database error" defensive branches (as opposed to the expected
	// pgx.ErrNoRows path every other fakeQuerier method already models),
	// which a real Postgres-backed integration test has no reliable way to
	// trigger on demand either.
	failOnce map[string]error
}

func newFakeQuerier() *fakeQuerier {
	return &fakeQuerier{
		users:         map[string]storedb.User{},
		usersByEmail:  map[string]string{},
		devices:       map[string]storedb.Device{},
		refreshTokens: map[string]storedb.RefreshToken{},
		tokensByHash:  map[string]string{},
		failOnce:      map[string]error{},
	}
}

// failNextCallTo arranges for the next call to the named fakeQuerier
// method to return err instead of its normal result.
func (f *fakeQuerier) failNextCallTo(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOnce[method] = err
}

// takeInjectedFailure returns (true, err) exactly once per failNextCallTo
// call for method, and (false, nil) otherwise.
func (f *fakeQuerier) takeInjectedFailure(method string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	err, ok := f.failOnce[method]
	if ok {
		delete(f.failOnce, method)
	}
	return ok, err
}

func (f *fakeQuerier) nextSeq() int64 {
	f.seq++
	return f.seq
}

func newUUID() pgtype.UUID {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return pgtype.UUID{Bytes: b, Valid: true}
}

func hashKey(h []byte) string { return fmt.Sprintf("%x", h) }

func (f *fakeQuerier) CreateDevice(_ context.Context, arg storedb.CreateDeviceParams) (storedb.Device, error) {
	if ok, err := f.takeInjectedFailure("CreateDevice"); ok {
		return storedb.Device{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	d := storedb.Device{
		ID:         newUUID(),
		UserID:     arg.UserID,
		Platform:   arg.Platform,
		Name:       arg.Name,
		Model:      arg.Model,
		OsVersion:  arg.OsVersion,
		AppVersion: arg.AppVersion,
		LastSeenAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CreatedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	f.devices[d.ID.String()] = d
	return d, nil
}

func (f *fakeQuerier) CreateRefreshToken(_ context.Context, arg storedb.CreateRefreshTokenParams) (storedb.RefreshToken, error) {
	if ok, err := f.takeInjectedFailure("CreateRefreshToken"); ok {
		return storedb.RefreshToken{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	rt := storedb.RefreshToken{
		ID:        newUUID(),
		UserID:    arg.UserID,
		DeviceID:  arg.DeviceID,
		TokenHash: arg.TokenHash,
		FamilyID:  arg.FamilyID,
		IssuedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
		ExpiresAt: arg.ExpiresAt,
	}
	f.refreshTokens[rt.ID.String()] = rt
	f.tokensByHash[hashKey(arg.TokenHash)] = rt.ID.String()
	return rt, nil
}

func (f *fakeQuerier) CreateUser(_ context.Context, arg storedb.CreateUserParams) (storedb.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if arg.Email != nil {
		key := *arg.Email
		if _, exists := f.usersByEmail[key]; exists {
			return storedb.User{}, &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}
		}
	}

	u := storedb.User{
		ID:           newUUID(),
		Email:        arg.Email,
		PasswordHash: arg.PasswordHash,
		AppleUserID:  arg.AppleUserID,
		DisplayName:  arg.DisplayName,
		Role:         arg.Role,
		Timezone:     arg.Timezone,
		CreatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
		UpdatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
		ServerSeq:    f.nextSeq(),
	}
	f.users[u.ID.String()] = u
	if arg.Email != nil {
		f.usersByEmail[*arg.Email] = u.ID.String()
	}
	return u, nil
}

func (f *fakeQuerier) GetDeviceByID(_ context.Context, arg storedb.GetDeviceByIDParams) (storedb.Device, error) {
	if ok, err := f.takeInjectedFailure("GetDeviceByID"); ok {
		return storedb.Device{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	d, ok := f.devices[arg.ID.String()]
	if !ok || d.UserID != arg.UserID || d.RevokedAt.Valid {
		return storedb.Device{}, pgx.ErrNoRows
	}
	return d, nil
}

func (f *fakeQuerier) GetRefreshTokenByHash(_ context.Context, tokenHash []byte) (storedb.RefreshToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	id, ok := f.tokensByHash[hashKey(tokenHash)]
	if !ok {
		return storedb.RefreshToken{}, pgx.ErrNoRows
	}
	return f.refreshTokens[id], nil
}

// GetRefreshTokenByHashForUpdate and GetRefreshTokenByHashForUpdateNoWait
// are only meaningfully different from GetRefreshTokenByHash under real
// Postgres row locking (RIZ-32 M2's tx-based Refresh path, exercised
// against a real database in internal/auth/refresh_concurrency_test.go).
// fakeQuerier has no notion of concurrent transactions, so both simply
// delegate to the same in-memory lookup; Service only takes the
// tx/row-locking path when Pool is non-nil, which unit tests using
// fakeQuerier never set.
func (f *fakeQuerier) GetRefreshTokenByHashForUpdate(ctx context.Context, tokenHash []byte) (storedb.RefreshToken, error) {
	return f.GetRefreshTokenByHash(ctx, tokenHash)
}

func (f *fakeQuerier) GetRefreshTokenByHashForUpdateNoWait(ctx context.Context, tokenHash []byte) (storedb.RefreshToken, error) {
	return f.GetRefreshTokenByHash(ctx, tokenHash)
}

// The internal/sync package's Querier methods below are not exercised by
// any internal/auth test (auth's Service never calls them); they exist
// only so *fakeQuerier continues to satisfy storedb.Querier, which is one
// interface shared by every package that talks to storedb.
func (f *fakeQuerier) CreateApp(context.Context, storedb.CreateAppParams) (storedb.App, error) {
	return storedb.App{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetAppByBundleID(context.Context, storedb.GetAppByBundleIDParams) (storedb.App, error) {
	return storedb.App{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetFocusSessionByID(context.Context, pgtype.UUID) (storedb.FocusSession, error) {
	return storedb.FocusSession{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetProjectByIDForUser(context.Context, storedb.GetProjectByIDForUserParams) (storedb.Project, error) {
	return storedb.Project{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetProjectForUser(context.Context, storedb.GetProjectForUserParams) (storedb.Project, error) {
	return storedb.Project{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetUserAppSettingByUserAndApp(context.Context, storedb.GetUserAppSettingByUserAndAppParams) (storedb.UserAppSetting, error) {
	return storedb.UserAppSetting{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) InsertActivityEvent(context.Context, storedb.InsertActivityEventParams) (storedb.ActivityEvent, error) {
	return storedb.ActivityEvent{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) TombstoneActivityEvent(context.Context, storedb.TombstoneActivityEventParams) (storedb.ActivityEvent, error) {
	return storedb.ActivityEvent{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) UpsertFocusSession(context.Context, storedb.UpsertFocusSessionParams) (storedb.FocusSession, error) {
	return storedb.FocusSession{}, errNotImplementedByFakeQuerier
}

// The internal/projects, internal/tags, internal/categories,
// internal/focussessions, and RIZ-34 pull-side Querier methods below are
// likewise not exercised by any internal/auth test; they exist only so
// *fakeQuerier continues to satisfy storedb.Querier.
func (f *fakeQuerier) CreateCategoryForUser(context.Context, storedb.CreateCategoryForUserParams) (storedb.Category, error) {
	return storedb.Category{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) CreateFocusSessionForUser(context.Context, storedb.CreateFocusSessionForUserParams) (storedb.FocusSession, error) {
	return storedb.FocusSession{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) CreateProjectForUser(context.Context, storedb.CreateProjectForUserParams) (storedb.Project, error) {
	return storedb.Project{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) CreateTagForUser(context.Context, storedb.CreateTagForUserParams) (storedb.Tag, error) {
	return storedb.Tag{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetCategoryForUser(context.Context, storedb.GetCategoryForUserParams) (storedb.Category, error) {
	return storedb.Category{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetFocusSessionForUser(context.Context, storedb.GetFocusSessionForUserParams) (storedb.FocusSession, error) {
	return storedb.FocusSession{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetOwnCategoryForUser(context.Context, storedb.GetOwnCategoryForUserParams) (storedb.Category, error) {
	return storedb.Category{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetTagForUser(context.Context, storedb.GetTagForUserParams) (storedb.Tag, error) {
	return storedb.Tag{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) ListCategoriesForUser(context.Context, storedb.ListCategoriesForUserParams) ([]storedb.Category, error) {
	return nil, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) ListFocusSessionsForUser(context.Context, storedb.ListFocusSessionsForUserParams) ([]storedb.FocusSession, error) {
	return nil, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) ListActivityEventsForUser(context.Context, storedb.ListActivityEventsForUserParams) ([]storedb.ActivityEvent, error) {
	return nil, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) RawActivityEventsForReport(context.Context, storedb.RawActivityEventsForReportParams) ([]storedb.RawActivityEventsForReportRow, error) {
	return nil, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) CategoryTotalsForRange(context.Context, storedb.CategoryTotalsForRangeParams) ([]storedb.CategoryTotalsForRangeRow, error) {
	return nil, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) AppTotalsForRange(context.Context, storedb.AppTotalsForRangeParams) ([]storedb.AppTotalsForRangeRow, error) {
	return nil, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) ListProjectsForUser(context.Context, storedb.ListProjectsForUserParams) ([]storedb.Project, error) {
	return nil, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) ListTagsForUser(context.Context, storedb.ListTagsForUserParams) ([]storedb.Tag, error) {
	return nil, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) ListChangelogPage(context.Context, storedb.ListChangelogPageParams) ([]storedb.ListChangelogPageRow, error) {
	return nil, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) SelectChangelogPruneBatch(context.Context, storedb.SelectChangelogPruneBatchParams) ([]storedb.SelectChangelogPruneBatchRow, error) {
	return nil, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) DeleteChangelogRows(context.Context, []int64) (int64, error) {
	return 0, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) AdvanceChangelogHorizon(context.Context, storedb.AdvanceChangelogHorizonParams) (storedb.AdvanceChangelogHorizonRow, error) {
	return storedb.AdvanceChangelogHorizonRow{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetChangelogHorizon(context.Context) (storedb.GetChangelogHorizonRow, error) {
	return storedb.GetChangelogHorizonRow{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetActivityEventForChangelogEntry(context.Context, storedb.GetActivityEventForChangelogEntryParams) (storedb.GetActivityEventForChangelogEntryRow, error) {
	return storedb.GetActivityEventForChangelogEntryRow{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetFocusSessionForChangelogEntry(context.Context, storedb.GetFocusSessionForChangelogEntryParams) (storedb.GetFocusSessionForChangelogEntryRow, error) {
	return storedb.GetFocusSessionForChangelogEntryRow{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetProjectForChangelogEntry(context.Context, storedb.GetProjectForChangelogEntryParams) (storedb.GetProjectForChangelogEntryRow, error) {
	return storedb.GetProjectForChangelogEntryRow{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetTagForChangelogEntry(context.Context, storedb.GetTagForChangelogEntryParams) (storedb.GetTagForChangelogEntryRow, error) {
	return storedb.GetTagForChangelogEntryRow{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetUserAppSettingForChangelogEntry(context.Context, storedb.GetUserAppSettingForChangelogEntryParams) (storedb.UserAppSetting, error) {
	return storedb.UserAppSetting{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetCategoryForChangelogEntry(context.Context, storedb.GetCategoryForChangelogEntryParams) (storedb.GetCategoryForChangelogEntryRow, error) {
	return storedb.GetCategoryForChangelogEntryRow{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) SoftDeleteFocusSessionForUser(context.Context, storedb.SoftDeleteFocusSessionForUserParams) (int64, error) {
	return 0, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) SoftDeleteOwnCategoryForUser(context.Context, storedb.SoftDeleteOwnCategoryForUserParams) (int64, error) {
	return 0, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) SoftDeleteProjectForUser(context.Context, storedb.SoftDeleteProjectForUserParams) (int64, error) {
	return 0, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) SoftDeleteTagForUser(context.Context, storedb.SoftDeleteTagForUserParams) (int64, error) {
	return 0, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) UpdateFocusSessionForUser(context.Context, storedb.UpdateFocusSessionForUserParams) (storedb.FocusSession, error) {
	return storedb.FocusSession{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) UpdateOwnCategoryForUser(context.Context, storedb.UpdateOwnCategoryForUserParams) (storedb.Category, error) {
	return storedb.Category{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) UpdateProjectForUser(context.Context, storedb.UpdateProjectForUserParams) (storedb.Project, error) {
	return storedb.Project{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) UpdateTagForUser(context.Context, storedb.UpdateTagForUserParams) (storedb.Tag, error) {
	return storedb.Tag{}, errNotImplementedByFakeQuerier
}

func (f *fakeQuerier) GetUserByAppleUserID(_ context.Context, appleUserID *string) (storedb.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.AppleUserID != nil && appleUserID != nil && *u.AppleUserID == *appleUserID {
			return u, nil
		}
	}
	return storedb.User{}, pgx.ErrNoRows
}

func (f *fakeQuerier) GetUserByEmail(_ context.Context, email *string) (storedb.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if email == nil {
		return storedb.User{}, pgx.ErrNoRows
	}
	id, ok := f.usersByEmail[*email]
	if !ok {
		return storedb.User{}, pgx.ErrNoRows
	}
	u := f.users[id]
	if u.DeletedAt.Valid {
		return storedb.User{}, pgx.ErrNoRows
	}
	return u, nil
}

func (f *fakeQuerier) GetUserByID(_ context.Context, id pgtype.UUID) (storedb.User, error) {
	if ok, err := f.takeInjectedFailure("GetUserByID"); ok {
		return storedb.User{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id.String()]
	if !ok || u.DeletedAt.Valid {
		return storedb.User{}, pgx.ErrNoRows
	}
	return u, nil
}

func (f *fakeQuerier) ListActiveRefreshTokensByUser(_ context.Context, userID pgtype.UUID) ([]storedb.RefreshToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []storedb.RefreshToken
	for _, rt := range f.refreshTokens {
		if rt.UserID == userID && !rt.RevokedAt.Valid && rt.ExpiresAt.Time.After(time.Now()) {
			out = append(out, rt)
		}
	}
	return out, nil
}

func (f *fakeQuerier) ListDevicesByUser(_ context.Context, userID pgtype.UUID) ([]storedb.Device, error) {
	if ok, err := f.takeInjectedFailure("ListDevicesByUser"); ok {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []storedb.Device
	for _, d := range f.devices {
		if d.UserID == userID && !d.RevokedAt.Valid {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeQuerier) RevokeDevice(_ context.Context, arg storedb.RevokeDeviceParams) error {
	if ok, err := f.takeInjectedFailure("RevokeDevice"); ok {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[arg.ID.String()]
	if !ok || d.UserID != arg.UserID || d.RevokedAt.Valid {
		return nil
	}
	d.RevokedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	f.devices[arg.ID.String()] = d
	return nil
}

func (f *fakeQuerier) RevokeRefreshTokenFamily(_ context.Context, familyID pgtype.UUID) error {
	if ok, err := f.takeInjectedFailure("RevokeRefreshTokenFamily"); ok {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, rt := range f.refreshTokens {
		if rt.FamilyID == familyID && !rt.RevokedAt.Valid {
			rt.RevokedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
			f.refreshTokens[id] = rt
		}
	}
	return nil
}

func (f *fakeQuerier) RevokeRefreshTokenFamilyForUser(_ context.Context, arg storedb.RevokeRefreshTokenFamilyForUserParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, rt := range f.refreshTokens {
		if rt.FamilyID == arg.FamilyID && rt.UserID == arg.UserID && !rt.RevokedAt.Valid {
			rt.RevokedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
			f.refreshTokens[id] = rt
		}
	}
	return nil
}

func (f *fakeQuerier) RevokeRefreshTokensByDevice(_ context.Context, arg storedb.RevokeRefreshTokensByDeviceParams) error {
	if ok, err := f.takeInjectedFailure("RevokeRefreshTokensByDevice"); ok {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, rt := range f.refreshTokens {
		if rt.DeviceID == arg.DeviceID && rt.UserID == arg.UserID && !rt.RevokedAt.Valid {
			rt.RevokedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
			f.refreshTokens[id] = rt
		}
	}
	return nil
}

func (f *fakeQuerier) RotateRefreshToken(_ context.Context, arg storedb.RotateRefreshTokenParams) (storedb.RefreshToken, error) {
	if ok, err := f.takeInjectedFailure("RotateRefreshToken"); ok {
		return storedb.RefreshToken{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rt, ok := f.refreshTokens[arg.ID.String()]
	if !ok || rt.RevokedAt.Valid {
		return storedb.RefreshToken{}, pgx.ErrNoRows
	}
	rt.RevokedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	rt.ReplacedBy = arg.ReplacedBy
	f.refreshTokens[arg.ID.String()] = rt
	return rt, nil
}

func (f *fakeQuerier) SoftDeleteUser(_ context.Context, id pgtype.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id.String()]
	if !ok {
		return nil
	}
	u.DeletedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	f.users[id.String()] = u
	return nil
}

func (f *fakeQuerier) TouchDeviceLastSeen(_ context.Context, arg storedb.TouchDeviceLastSeenParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[arg.ID.String()]
	if !ok || d.UserID != arg.UserID || d.RevokedAt.Valid {
		return nil
	}
	d.LastSeenAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	f.devices[arg.ID.String()] = d
	return nil
}

func (f *fakeQuerier) UpdateDeviceMetadata(_ context.Context, arg storedb.UpdateDeviceMetadataParams) (storedb.Device, error) {
	if ok, err := f.takeInjectedFailure("UpdateDeviceMetadata"); ok {
		return storedb.Device{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[arg.ID.String()]
	if !ok || d.UserID != arg.UserID || d.RevokedAt.Valid {
		return storedb.Device{}, pgx.ErrNoRows
	}
	d.Name = arg.Name
	d.Model = arg.Model
	d.OsVersion = arg.OsVersion
	d.AppVersion = arg.AppVersion
	d.LastSeenAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	f.devices[arg.ID.String()] = d
	return d, nil
}

func (f *fakeQuerier) UpdateDeviceName(_ context.Context, arg storedb.UpdateDeviceNameParams) (storedb.Device, error) {
	if ok, err := f.takeInjectedFailure("UpdateDeviceName"); ok {
		return storedb.Device{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[arg.ID.String()]
	if !ok || d.UserID != arg.UserID || d.RevokedAt.Valid {
		return storedb.Device{}, pgx.ErrNoRows
	}
	d.Name = arg.Name
	f.devices[arg.ID.String()] = d
	return d, nil
}

func (f *fakeQuerier) UpdateUserProfile(_ context.Context, arg storedb.UpdateUserProfileParams) (storedb.User, error) {
	if ok, err := f.takeInjectedFailure("UpdateUserProfile"); ok {
		return storedb.User{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[arg.ID.String()]
	if !ok || u.DeletedAt.Valid {
		return storedb.User{}, pgx.ErrNoRows
	}
	u.DisplayName = arg.DisplayName
	u.Timezone = arg.Timezone
	u.ServerSeq = f.nextSeq()
	f.users[arg.ID.String()] = u
	return u, nil
}

// RecordFailedLoginAttempt mirrors login_lockout.sql's
// RecordFailedLoginAttempt: it increments failed_login_attempts and, if the
// post-increment count reaches arg.Threshold, escalates lockout_count and
// sets locked_until to arg.Now plus a doubling-per-prior-lockout duration
// capped at MaxDurationSeconds — computed from the row's own
// pre-increment lockout_count, matching the real query's single-statement
// atomicity (this fake holds f.mu for the whole operation, so concurrent
// callers in the same test process serialize the same way a real UPDATE's
// row lock would).
func (f *fakeQuerier) RecordFailedLoginAttempt(_ context.Context, arg storedb.RecordFailedLoginAttemptParams) (storedb.User, error) {
	if hit, err := f.takeInjectedFailure("RecordFailedLoginAttempt"); hit {
		return storedb.User{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	u, ok := f.users[arg.ID.String()]
	if !ok || u.DeletedAt.Valid {
		return storedb.User{}, pgx.ErrNoRows
	}

	u.FailedLoginAttempts++
	notCurrentlyLocked := !u.LockedUntil.Valid || !u.LockedUntil.Time.After(arg.Now.Time)
	if notCurrentlyLocked && u.FailedLoginAttempts >= arg.Threshold {
		// Guarded on "not currently locked" to mirror login_lockout.sql's
		// fix (RIZ-59 review, MEDIUM finding): without this guard,
		// concurrent failed attempts landing after the row is already
		// locked would each re-escalate lockout_count once per attempt
		// instead of once per lockout episode.
		duration := arg.BaseDurationSeconds * math.Pow(2, float64(u.LockoutCount))
		if duration > arg.MaxDurationSeconds {
			duration = arg.MaxDurationSeconds
		}
		u.LockoutCount++
		u.LockedUntil = pgtype.Timestamptz{
			Time:  arg.Now.Time.Add(time.Duration(duration * float64(time.Second))),
			Valid: true,
		}
	}
	u.UpdatedAt = arg.Now

	f.users[arg.ID.String()] = u
	return u, nil
}

// ResetLoginLockout mirrors login_lockout.sql's ResetLoginLockout: it
// clears failed_login_attempts, lockout_count, and locked_until on a
// successful login.
func (f *fakeQuerier) ResetLoginLockout(_ context.Context, arg storedb.ResetLoginLockoutParams) error {
	if hit, err := f.takeInjectedFailure("ResetLoginLockout"); hit {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	u, ok := f.users[arg.ID.String()]
	if !ok || u.DeletedAt.Valid {
		return pgx.ErrNoRows
	}

	u.FailedLoginAttempts = 0
	u.LockoutCount = 0
	u.LockedUntil = pgtype.Timestamptz{}
	u.UpdatedAt = arg.Now

	f.users[arg.ID.String()] = u
	return nil
}

var _ storedb.Querier = (*fakeQuerier)(nil)
