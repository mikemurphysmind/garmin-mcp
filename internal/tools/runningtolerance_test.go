package tools

import (
	"net/http"
	"testing"

	"github.com/tamcore/garmin-mcp/internal/garmin/client"
	"github.com/tamcore/garmin-mcp/internal/testkit"
)

// Every running-tolerance fixture here is synthetic: the loads, distances and labels
// are invented and none is a recording of a real account.
const (
	toleranceDay       = "2026-02-01"
	toleranceStartDate = scoresStartDate
	toleranceEndDate   = scoresEndDate
)

// dailyToleranceBody is the daily aggregation's own field spelling, in metres.
const dailyToleranceBody = `[{"calendarDate":"` + toleranceDay + `","acuteTolerance":64200,` +
	`"acuteImpactLoad":48150,"acuteDistance":42000,` +
	`"runningToleranceFeedBackPhrase":"PRODUCTIVE"}]`

// weeklyToleranceBody is the weekly aggregation, whose three quantities carry
// different names for the same measurements. The periods arrive out of order, which is
// what Garmin does, and one distance arrives as a numeric string.
const weeklyToleranceBody = `[{"calendarDate":"2026-01-19","tolerance":61000,` +
	`"totalImpactLoad":45000,"totalDistance":"40000","startOfWeek":"2026-01-19",` +
	`"endOfWeek":"2026-01-25","weekIndex":3},` +
	`{"calendarDate":"2026-01-12","tolerance":58000,"totalImpactLoad":40000,` +
	`"totalDistance":36000,"startOfWeek":"2026-01-12","endOfWeek":"2026-01-18",` +
	`"weekIndex":2}]`

// toleranceScript scripts the one endpoint both reads ask.
func toleranceScript(body string) testkit.Script {
	return testkit.NewScript().With(client.PathRunningToleranceStats,
		testkit.JSON(http.StatusOK, body))
}

func TestGetRunningToleranceReturnsTheDaysCapacity(t *testing.T) {
	t.Parallel()

	h := newToolHarness(t, toleranceScript(dailyToleranceBody))

	result := h.call(t, ToolGetRunningTolerance, map[string]any{argDate: toleranceDay})

	if supported, _ := result["supported"].(bool); !supported {
		t.Fatalf("supported = false, want true: %v", result)
	}
	if got := number(t, result, "tolerance_km"); got != 64.2 {
		t.Errorf("tolerance_km = %v, want the metres converted to 64.2", got)
	}
	if got := number(t, result, "acute_load_km"); got != 48.15 {
		t.Errorf("acute_load_km = %v, want 48.15", got)
	}
	if got := number(t, result, "distance_km"); got != 42 {
		t.Errorf("distance_km = %v, want 42", got)
	}
	// 48150 / 42000, rounded to two decimals as upstream rounds it.
	if got := number(t, result, "load_ratio"); got != 1.15 {
		t.Errorf("load_ratio = %v, want 1.15", got)
	}
	if got, _ := result["feedback_phrase"].(string); got != "PRODUCTIVE" {
		t.Errorf("feedback_phrase = %q, want Garmin's own label", got)
	}
}

// TestGetRunningToleranceSendsTheDailyAggregation pins the day the single-day read
// asks for and the aggregation it sends, both of which the endpoint takes as
// parameters rather than as path segments.
func TestGetRunningToleranceSendsTheDailyAggregation(t *testing.T) {
	t.Parallel()

	h := newToolHarness(t, toleranceScript(dailyToleranceBody))

	h.call(t, ToolGetRunningTolerance, map[string]any{argDate: toleranceDay})

	requests := h.fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("dispatched %d requests, want one", len(requests))
	}
	query := requests[0].Query
	if got := query.Get(client.QueryAggregation); got != client.AggregationDaily {
		t.Errorf("aggregation = %q, want daily", got)
	}
	if got := query.Get(client.QueryStartDate); got != toleranceDay {
		t.Errorf("startDate = %q, want %q", got, toleranceDay)
	}
	if got := query.Get(client.QueryEndDate); got != toleranceDay {
		t.Errorf("endDate = %q, want %q", got, toleranceDay)
	}
}

// TestGetRunningToleranceReportsAnUnsupportedDevice proves an account whose device
// does not report the metric is told so, rather than shown a day of zeroes.
func TestGetRunningToleranceReportsAnUnsupportedDevice(t *testing.T) {
	t.Parallel()

	h := newToolHarness(t, toleranceScript(`[]`))

	result := h.call(t, ToolGetRunningTolerance, map[string]any{argDate: toleranceDay})

	if supported, _ := result["supported"].(bool); supported {
		t.Error("supported = true, want false for an empty answer")
	}
	if note, _ := result["note"].(string); note == "" {
		t.Error("no note explains the absent reading")
	}
	if _, present := result["tolerance_km"]; present {
		t.Error("a capacity was reported for a device that reports none")
	}
}

func TestGetRunningToleranceTrendOrdersThePeriodsOldestFirst(t *testing.T) {
	t.Parallel()

	h := newToolHarness(t, toleranceScript(weeklyToleranceBody))

	result := h.call(t, ToolGetRunningToleranceTrend, map[string]any{
		argStartDate: toleranceStartDate, argEndDate: toleranceEndDate,
	})

	if got, _ := result["aggregation"].(string); got != client.AggregationWeekly {
		t.Errorf("aggregation = %q, want the default weekly", got)
	}
	points := list(t, result, "trend")
	if len(points) != 2 {
		t.Fatalf("trend holds %d periods, want two", len(points))
	}
	// Garmin answered newest first; the result is ordered oldest first.
	if got, _ := entry(t, points, 0)["date"].(string); got != "2026-01-12" {
		t.Errorf("trend[0].date = %q, want the older period first", got)
	}
	if got := number(t, entry(t, points, 0), "distance_km"); got != 36 {
		t.Errorf("trend[0].distance_km = %v, want 36", got)
	}
	// The later period's distance arrives as a numeric string and still decodes.
	if got := number(t, entry(t, points, 1), "distance_km"); got != 40 {
		t.Errorf("trend[1].distance_km = %v, want 40 from the string form", got)
	}
	if got := number(t, entry(t, points, 1), "week_index"); got != 3 {
		t.Errorf("trend[1].week_index = %v, want 3", got)
	}
	if got := number(t, result, "first_tolerance_km"); got != 58 {
		t.Errorf("first_tolerance_km = %v, want the oldest period's 58", got)
	}
	if got := number(t, result, "latest_tolerance_km"); got != 61 {
		t.Errorf("latest_tolerance_km = %v, want the newest period's 61", got)
	}
	if got := number(t, result, "tolerance_change_km"); got != 3 {
		t.Errorf("tolerance_change_km = %v, want 3", got)
	}
}

// TestGetRunningToleranceTrendSendsTheRequestedAggregation proves the caller's choice
// reaches the query string.
func TestGetRunningToleranceTrendSendsTheRequestedAggregation(t *testing.T) {
	t.Parallel()

	h := newToolHarness(t, toleranceScript(dailyToleranceBody))

	h.call(t, ToolGetRunningToleranceTrend, map[string]any{
		argStartDate: toleranceStartDate, argEndDate: toleranceEndDate,
		argNameAggregation: client.AggregationDaily,
	})

	requests := h.fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("dispatched %d requests, want one", len(requests))
	}
	if got := requests[0].Query.Get(client.QueryAggregation); got != client.AggregationDaily {
		t.Errorf("aggregation = %q, want daily", got)
	}
}

// TestGetRunningToleranceTrendRefusesUnacceptedArguments keeps validation at the
// boundary: nothing is dispatched for an aggregation Garmin does not accept, a
// reversed window, or a window past the aggregation's own bound.
func TestGetRunningToleranceTrendRefusesUnacceptedArguments(t *testing.T) {
	t.Parallel()

	for name, args := range map[string]map[string]any{
		"unknown aggregation": {
			argStartDate: toleranceStartDate, argEndDate: toleranceEndDate,
			argNameAggregation: "monthly",
		},
		"reversed window": {
			argStartDate: toleranceEndDate, argEndDate: toleranceStartDate,
		},
		"window past the daily bound": {
			argStartDate: "2025-01-01", argEndDate: scoresEndDate,
			argNameAggregation: client.AggregationDaily,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newToolHarness(t, toleranceScript(dailyToleranceBody))

			if err := h.callError(t, ToolGetRunningToleranceTrend, args); err == "" {
				t.Errorf("%s was accepted", name)
			}
			if got := len(h.fake.Requests()); got != 0 {
				t.Errorf("dispatched %d requests for a refused argument, want none", got)
			}
		})
	}
}

// TestRunningToleranceLogValuesReportShapeOnly keeps a health reading out of the logs.
func TestRunningToleranceLogValuesReportShapeOnly(t *testing.T) {
	t.Parallel()

	capacity := 64.2
	day := RunningTolerance{Date: toleranceDay, Supported: true, ToleranceKM: &capacity}
	trend := RunningToleranceTrend{
		Aggregation: client.AggregationWeekly, Supported: true,
		Trend: []RunningTolerancePoint{{ToleranceKM: &capacity}},
	}

	for _, rendered := range []string{day.LogValue().String(), trend.LogValue().String()} {
		if contains(rendered, "64.2") {
			t.Errorf("the log value %q carries the reading", rendered)
		}
	}
}
