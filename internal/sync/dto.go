package sync

import "encoding/json"

// pushRequest mirrors documentation/sync-protocol.md §Push's request
// schema.
type pushRequest struct {
	DeviceID string     `json:"device_id"`
	Items    []pushItem `json:"items"`
}

// pushItem is one entry of the request's "items" array: an
// "entity_type" discriminator plus the entity-specific payload, decoded
// lazily (json.RawMessage) so an item with an unrecognized or malformed
// entity_type can still be reported as a per-item "invalid" result instead
// of failing the whole batch decode, per documentation/sync-protocol.md
// §Push ("Partial success is allowed").
type pushItem struct {
	EntityType string          `json:"entity_type"`
	Data       json.RawMessage `json:"data"`
}

// activityEventData mirrors the "activity_event" entity's "data" object
// from documentation/sync-protocol.md §Push, whose worked example does not
// include a `type` field. `type` (one of `app_active`/`idle`/`locked`/
// `mobile_usage`/`manual`, per database-schema.md's CHECK constraint) is
// kept in the wire schema as an additive, OPTIONAL field: a doc-conformant
// client that never sends `type` still works end-to-end (the service layer
// defaults an omitted `type` to `"manual"`, see service.go); a client that
// does send `type` gets it validated against the enum and rejected as
// "invalid" if it's outside it. `source` (`desktop`/`mobile`/`manual`) is
// NOT added to the wire payload: it is derived server-side from the
// authenticated device's `platform` column (`macos` -> `desktop`, `ios` ->
// `mobile`), since that already lives in `devices` and doesn't need to be
// re-supplied by the client.
type activityEventData struct {
	EventID     string  `json:"event_id"`
	StartedAt   string  `json:"started_at"`
	EndedAt     string  `json:"ended_at"`
	AppBundleID string  `json:"app_bundle_id"`
	WindowTitle *string `json:"window_title"`
	Precision   string  `json:"precision"`
	Type        *string `json:"type,omitempty"`
	Deleted     bool    `json:"deleted"`
}

// focusSessionData mirrors the "focus_session" entity's "data" object from
// documentation/sync-protocol.md §Push's worked example, which already
// uses `kind`, `status`, and `note` — the same field names as
// documentation/database-schema.md's `focus_sessions` table. `id` and
// `updated_at` are required per the doc ("For mutable entities, data.id and
// data.updated_at are required on every item, including tombstones").
// `planned_duration_s` is an additive field (present in database-schema.md
// but not shown in the doc's worked example) needed to populate that
// column; it is optional here so a doc-conformant client that omits it
// still works.
type focusSessionData struct {
	ID               string  `json:"id"`
	UpdatedAt        string  `json:"updated_at"`
	StartedAt        string  `json:"started_at"`
	EndedAt          *string `json:"ended_at"`
	ProjectID        string  `json:"project_id"`
	Kind             string  `json:"kind"`
	Status           string  `json:"status"`
	PlannedDurationS *int32  `json:"planned_duration_s"`
	Note             *string `json:"note"`
	Deleted          bool    `json:"deleted"`
}

// pushResponse / pushResult mirror documentation/sync-protocol.md §Push's
// response schema: one result per submitted item, in request order.
type pushResponse struct {
	Results []pushResult `json:"results"`
}

// pushResult is one entry of the response's "results" array. EventID is
// populated for "activity_event" items, ID for every other entity type,
// per the doc's worked example. ServerSeq is populated only for an
// "applied" result on a mutable (last-write-wins) entity, matching the
// doc's example (the "applied" activity_event result in the doc carries no
// server_seq; the "applied" focus_session result does).
type pushResult struct {
	Index      int              `json:"index"`
	EntityType string           `json:"entity_type"`
	EventID    string           `json:"event_id,omitempty"`
	ID         string           `json:"id,omitempty"`
	Status     string           `json:"status"`
	ServerSeq  *int64           `json:"server_seq,omitempty"`
	Error      *pushResultError `json:"error,omitempty"`
}

// pushResultError is the "error" object on an "invalid" pushResult.
type pushResultError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func appliedActivityEvent(index int, eventID string) pushResult {
	return pushResult{Index: index, EntityType: "activity_event", EventID: eventID, Status: "applied"}
}

func duplicateActivityEvent(index int, eventID string) pushResult {
	return pushResult{Index: index, EntityType: "activity_event", EventID: eventID, Status: "duplicate"}
}

func invalidActivityEvent(index int, eventID, code, message string) pushResult {
	return pushResult{
		Index: index, EntityType: "activity_event", EventID: eventID, Status: "invalid",
		Error: &pushResultError{Code: code, Message: message},
	}
}

func appliedFocusSession(index int, id string, serverSeq int64) pushResult {
	return pushResult{Index: index, EntityType: "focus_session", ID: id, Status: "applied", ServerSeq: &serverSeq}
}

func duplicateFocusSession(index int, id string) pushResult {
	return pushResult{Index: index, EntityType: "focus_session", ID: id, Status: "duplicate"}
}

func invalidFocusSession(index int, id, code, message string) pushResult {
	return pushResult{
		Index: index, EntityType: "focus_session", ID: id, Status: "invalid",
		Error: &pushResultError{Code: code, Message: message},
	}
}

func invalidUnsupportedEntity(index int, entityType string) pushResult {
	return pushResult{
		Index: index, EntityType: entityType, Status: "invalid",
		Error: &pushResultError{
			Code:    "UNSUPPORTED_ENTITY_TYPE",
			Message: "entity_type \"" + entityType + "\" is not yet supported by this endpoint",
		},
	}
}
