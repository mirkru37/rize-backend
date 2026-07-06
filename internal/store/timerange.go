package store

import (
	"errors"
	"time"
)

// MaxReportRange bounds how wide a [from, to) time range a caller may
// request from GET /v1/activities or any GET /v1/reports/* endpoint.
// documentation/api-reference.md does not pin an exact bound ("reject
// absurd ranges with 400" is the only requirement carried in the RIZ-35
// brief); one year is chosen here as a generous but finite ceiling that
// still prevents an unbounded full-hypertable scan from a single request.
const MaxReportRange = 366 * 24 * time.Hour

// ValidateRange checks that from <= to and that the range does not exceed
// MaxReportRange, per this ticket's time-range validation requirement.
func ValidateRange(from, to time.Time) error {
	if from.After(to) {
		return errors.New("from must not be after to")
	}
	if to.Sub(from) > MaxReportRange {
		return errors.New("requested range exceeds the maximum allowed range of 366 days")
	}
	return nil
}
