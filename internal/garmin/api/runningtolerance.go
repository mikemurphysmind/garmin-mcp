package api

import (
	"context"
	"fmt"
	"net/url"

	"github.com/tamcore/garmin-mcp/internal/garmin/client"
)

// The running-tolerance read: Garmin's running load-capacity model. One endpoint
// serves both aggregations, and the two answer with different field names for the
// same three quantities, which is why both spellings are decoded here.
//
// Source: get_running_tolerance in python-garminconnect 0.3.10, which sends
// startDate, endDate and aggregation to
// "/metrics-service/metrics/runningtolerance/stats", and the field sets upstream's
// get_running_tolerance and get_running_tolerance_trend read (training.py:1372-1374
// for the daily spellings and training.py:1449-1451 for the weekly ones).

// A RunningToleranceEntry is one period of the running-tolerance model.
//
// It is health material: it says how much running load a person can absorb, so it is
// never logged. Every field is optional, and the numbers decode through the union
// decoder because Garmin has been seen sending them as strings.
//
// Garmin reports the three load quantities in metres and names them differently per
// aggregation: a daily period carries acuteTolerance, acuteImpactLoad and
// acuteDistance, a weekly one tolerance, totalImpactLoad and totalDistance. Both are
// decoded, and the accessors report whichever the answer carried.
type RunningToleranceEntry struct {
	CalendarDate *string `json:"calendarDate"`

	AcuteTolerance  client.Number `json:"acuteTolerance"`
	AcuteImpactLoad client.Number `json:"acuteImpactLoad"`
	AcuteDistance   client.Number `json:"acuteDistance"`

	Tolerance       client.Number `json:"tolerance"`
	TotalImpactLoad client.Number `json:"totalImpactLoad"`
	TotalDistance   client.Number `json:"totalDistance"`

	FeedbackPhrase client.Text `json:"runningToleranceFeedBackPhrase"`

	StartOfWeek *string       `json:"startOfWeek"`
	EndOfWeek   *string       `json:"endOfWeek"`
	WeekIndex   client.Number `json:"weekIndex"`
}

// ToleranceMeters reports the load capacity, from whichever field the aggregation
// carried.
func (r RunningToleranceEntry) ToleranceMeters() (float64, bool) {
	return firstSetNumber(r.AcuteTolerance, r.Tolerance)
}

// ImpactLoadMeters reports the intensity-adjusted load.
func (r RunningToleranceEntry) ImpactLoadMeters() (float64, bool) {
	return firstSetNumber(r.AcuteImpactLoad, r.TotalImpactLoad)
}

// DistanceMeters reports the raw distance behind the load.
func (r RunningToleranceEntry) DistanceMeters() (float64, bool) {
	return firstSetNumber(r.AcuteDistance, r.TotalDistance)
}

// RunningTolerance reads the running-tolerance statistics for a window.
//
// Aggregation is a Garmin wire value: only client.AggregationDaily and
// client.AggregationWeekly are accepted, so nothing a caller typed reaches the query
// string unchecked. The window is validated against the configured date-range bound
// before anything is dispatched.
//
// An account whose device does not report the metric answers with an empty list. That
// is an empty result, not a failure.
func (t *TrainingTrends) RunningTolerance(
	ctx context.Context, session client.Session, span client.DateRange,
	aggregation string, op client.Op,
) ([]RunningToleranceEntry, error) {
	query := url.Values{}
	query.Set(client.QueryStartDate, span.Start().String())
	query.Set(client.QueryEndDate, span.End().String())
	query.Set(client.QueryAggregation, aggregation)

	req := readRequest(op, client.EndpointRunningTolerance,
		client.PathRunningToleranceStats, query)

	switch aggregation {
	case client.AggregationDaily, client.AggregationWeekly:
	default:
		return nil, invalid(req, fmt.Errorf(
			"%w: aggregation must be %q or %q", client.ErrValidation,
			client.AggregationDaily, client.AggregationWeekly))
	}
	if err := t.req.limits().ValidateDateRange(span); err != nil {
		return nil, invalid(req, err)
	}

	var list client.List[RunningToleranceEntry]
	if _, err := t.req.read(ctx, session, req, &list); err != nil {
		return nil, err
	}
	return list.Items(), nil
}
