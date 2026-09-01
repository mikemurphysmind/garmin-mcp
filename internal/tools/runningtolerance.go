package tools

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tamcore/garmin-mcp/internal/garmin/api"
	"github.com/tamcore/garmin-mcp/internal/garmin/client"
	"github.com/tamcore/garmin-mcp/internal/mcpserver"
	"github.com/tamcore/garmin-mcp/internal/policy"
)

// The two running-tolerance reads. Both are additions beyond the pinned manifest:
// upstream added them after the pinned commit. They share one endpoint, one result
// model and one curation, so they share a file.
const (
	ToolGetRunningTolerance      = "get_running_tolerance"
	ToolGetRunningToleranceTrend = "get_running_tolerance_trend"
)

// The window bounds the trend accepts, per aggregation. A daily window is capped
// tighter because it returns one entry a day; both bounds protect the result size
// rather than the request count, because the endpoint answers a whole window at once.
//
// Source: the max_days branch of upstream's get_running_tolerance_trend.
const (
	MaxRunningToleranceDailyDays  = 90
	MaxRunningToleranceWeeklyDays = 366
)

// argNameAggregation is the trend's aggregation argument.
const argNameAggregation = "aggregation"

// maxAggregationArgumentLen bounds the aggregation argument. The two accepted values
// are Garmin wire values, and the longer is six characters.
const maxAggregationArgumentLen = 8

// A RunningTolerancePoint is one period of the model, in kilometres.
//
// Garmin reports the three quantities in metres; they are reported here in kilometres
// so they are directly comparable, exactly as upstream reports them. LoadRatio is the
// impact load divided by the distance: how much intensity inflates the cost of each
// kilometre run.
type RunningTolerancePoint struct {
	Date        *string  `json:"date,omitempty" jsonschema:"the period's day, YYYY-MM-DD"`
	ToleranceKM *float64 `json:"tolerance_km,omitempty" jsonschema:"the load capacity, km"`
	AcuteLoadKM *float64 `json:"acute_load_km,omitempty" jsonschema:"the adjusted load, km"`
	DistanceKM  *float64 `json:"distance_km,omitempty" jsonschema:"the distance run, km"`
	LoadRatio   *float64 `json:"load_ratio,omitempty" jsonschema:"load over distance"`

	FeedbackPhrase *string `json:"feedback_phrase,omitempty" jsonschema:"Garmin's own label"`
	StartOfWeek    *string `json:"start_of_week,omitempty" jsonschema:"a week's first day"`
	EndOfWeek      *string `json:"end_of_week,omitempty" jsonschema:"a week's last day"`
	WeekIndex      *int    `json:"week_index,omitempty" jsonschema:"a week's index"`
}

// RunningTolerance is one day of the model. It is health data — never log it, never
// cache it.
type RunningTolerance struct {
	Date      string `json:"date" jsonschema:"the day that was read, YYYY-MM-DD"`
	Supported bool   `json:"supported" jsonschema:"whether the device reports the metric"`

	ToleranceKM *float64 `json:"tolerance_km,omitempty" jsonschema:"the load capacity, km"`
	AcuteLoadKM *float64 `json:"acute_load_km,omitempty" jsonschema:"the adjusted load, km"`
	DistanceKM  *float64 `json:"distance_km,omitempty" jsonschema:"the distance run, km"`
	LoadRatio   *float64 `json:"load_ratio,omitempty" jsonschema:"load over distance"`

	FeedbackPhrase *string `json:"feedback_phrase,omitempty" jsonschema:"Garmin's own label"`
	Note           string  `json:"note,omitempty" jsonschema:"why no reading is reported"`
}

// LogValue reports whether a reading exists, never the reading.
func (r RunningTolerance) LogValue() slog.Value {
	return shape("runningTolerance", slog.Bool("supported", r.Supported))
}

// RunningToleranceTrend is the model over an inclusive window.
type RunningToleranceTrend struct {
	StartDate   string `json:"start_date" jsonschema:"the inclusive first day, YYYY-MM-DD"`
	EndDate     string `json:"end_date" jsonschema:"the inclusive last day, YYYY-MM-DD"`
	Aggregation string `json:"aggregation" jsonschema:"daily or weekly, as requested"`
	DataPoints  int    `json:"data_points" jsonschema:"how many periods this result carries"`

	FirstToleranceKM  *float64 `json:"first_tolerance_km,omitempty" jsonschema:"the earliest capacity"`
	LatestToleranceKM *float64 `json:"latest_tolerance_km,omitempty" jsonschema:"the latest capacity"`
	ToleranceChangeKM *float64 `json:"tolerance_change_km,omitempty" jsonschema:"latest minus first"`

	Supported bool                    `json:"supported" jsonschema:"whether the device reports it"`
	Trend     []RunningTolerancePoint `json:"trend" jsonschema:"the periods, oldest first"`
	Note      string                  `json:"note,omitempty" jsonschema:"why no series is reported"`
}

// LogValue reports the shape of the series, never a reading.
func (r RunningToleranceTrend) LogValue() slog.Value {
	return shape("runningToleranceTrend",
		slog.Int("points", len(r.Trend)),
		slog.Bool("supported", r.Supported),
	)
}

// runningToleranceInput is the single-day argument set.
type runningToleranceInput struct {
	Date string `json:"date" jsonschema:"the calendar day to read, YYYY-MM-DD"`
}

// runningToleranceTrendInput is the window argument set.
type runningToleranceTrendInput struct {
	StartDate   string  `json:"start_date" jsonschema:"the inclusive first day, YYYY-MM-DD"`
	EndDate     string  `json:"end_date" jsonschema:"the inclusive last day, YYYY-MM-DD"`
	Aggregation *string `json:"aggregation,omitempty" jsonschema:"daily or weekly, default weekly"`
}

func getRunningToleranceContract() Contract {
	return Contract{
		Spec: mcpserver.ToolSpec{
			Name:  ToolGetRunningTolerance,
			Title: "Get the running tolerance",
			Description: "read Garmin's running load-capacity model for one day: how much " +
				"running load the account can currently absorb, the intensity-adjusted " +
				"load its recent runs produced, and the distance behind that load. All " +
				"three are in kilometres, so they are directly comparable, and load_ratio " +
				"says how much intensity is inflating the cost of each kilometre. A device " +
				"that does not report the metric answers supported=false",
			Tier:        policy.TierReadOnly,
			Category:    categoryHealth,
			Annotations: readOnlyAnnotations(),
		},
		Schema: NewSchema(dateProperty("date", "the calendar day to read")),
	}
}

func getRunningToleranceTrendContract() Contract {
	return Contract{
		Spec: mcpserver.ToolSpec{
			Name:  ToolGetRunningToleranceTrend,
			Title: "Get the running tolerance trend",
			Description: "read Garmin's running load-capacity model over an inclusive date " +
				"window. The metric moves slowly, so its value is in the trajectory rather " +
				"than in any single period: weekly aggregation gives a compact multi-month " +
				"view and daily gives day-to-day resolution over a shorter window. The " +
				"window may span " + strconv.Itoa(MaxRunningToleranceDailyDays) +
				" days daily and " + strconv.Itoa(MaxRunningToleranceWeeklyDays) +
				" weekly; the endpoint answers the whole window in one request, so those " +
				"bounds protect the result size rather than the request count",
			Tier:        policy.TierReadOnly,
			Category:    categoryHealth,
			Annotations: readOnlyAnnotations(),
		},
		Schema: NewSchema(
			dateProperty("start_date", "the inclusive first day of the window"),
			dateProperty("end_date", "the inclusive last day of the window"),
			Property{
				Name:        argNameAggregation,
				Types:       []string{typeString},
				Description: "the period length to aggregate by",
				Enum:        []any{client.AggregationWeekly, client.AggregationDaily},
				MaxLength:   new(maxAggregationArgumentLen),
				Default:     client.AggregationWeekly,
			},
		),
	}
}

// registerGetRunningTolerance registers the single-day read.
func registerGetRunningTolerance(registry *mcpserver.Registry, svc *service) error {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in runningToleranceInput) (
		*mcp.CallToolResult, RunningTolerance, error,
	) {
		date, err := parseCalendarDate("date", in.Date)
		if err != nil {
			return nil, RunningTolerance{}, err
		}
		span, err := client.NewDateRange(date, date)
		if err != nil {
			return nil, RunningTolerance{}, invalidArgument("date must be a real calendar date")
		}
		session, err := svc.session(ctx)
		if err != nil {
			return nil, RunningTolerance{}, err
		}

		entries, err := svc.trends().RunningTolerance(ctx, session, span,
			client.AggregationDaily, client.OpGetRunningTolerance)
		if err != nil {
			return nil, RunningTolerance{}, fail(err)
		}
		return nil, newRunningTolerance(date, entries), nil
	}
	return mcpserver.AddTool(registry, getRunningToleranceContract().Registration(), handler)
}

// registerGetRunningToleranceTrend registers the window read.
func registerGetRunningToleranceTrend(registry *mcpserver.Registry, svc *service) error {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in runningToleranceTrendInput) (
		*mcp.CallToolResult, RunningToleranceTrend, error,
	) {
		aggregation, err := parseAggregation(in.Aggregation)
		if err != nil {
			return nil, RunningToleranceTrend{}, err
		}
		span, err := parseRunningToleranceWindow(in.StartDate, in.EndDate, aggregation)
		if err != nil {
			return nil, RunningToleranceTrend{}, err
		}
		session, err := svc.session(ctx)
		if err != nil {
			return nil, RunningToleranceTrend{}, err
		}

		entries, err := svc.trends().RunningTolerance(ctx, session, span, aggregation,
			client.OpGetRunningToleranceTrend)
		if err != nil {
			return nil, RunningToleranceTrend{}, fail(err)
		}
		return nil, newRunningToleranceTrend(span, aggregation, entries), nil
	}
	return mcpserver.AddTool(registry, getRunningToleranceTrendContract().Registration(), handler)
}

// parseAggregation narrows the aggregation to the two Garmin wire values. An absent
// argument is weekly, which is upstream's default.
func parseAggregation(value *string) (string, error) {
	if value == nil {
		return client.AggregationWeekly, nil
	}
	switch *value {
	case client.AggregationDaily, client.AggregationWeekly:
		return *value, nil
	default:
		return "", invalidArgument(argNameAggregation + " must be daily or weekly")
	}
}

// parseRunningToleranceWindow validates the window against the bound of the requested
// aggregation, which is tighter than the request layer's own date-range bound for a
// daily read and wider for a weekly one.
func parseRunningToleranceWindow(
	startValue, endValue, aggregation string,
) (client.DateRange, error) {
	start, err := parseCalendarDate("start_date", startValue)
	if err != nil {
		return client.DateRange{}, err
	}
	end, err := parseCalendarDate("end_date", endValue)
	if err != nil {
		return client.DateRange{}, err
	}
	span, err := client.NewDateRange(start, end)
	if err != nil {
		return client.DateRange{}, invalidArgument("start_date must not be after end_date")
	}

	maxDays := MaxRunningToleranceWeeklyDays
	if aggregation == client.AggregationDaily {
		maxDays = MaxRunningToleranceDailyDays
	}
	if span.Days() > maxDays {
		return client.DateRange{}, invalidArgument("the window must not exceed " +
			strconv.Itoa(maxDays) + " days for " + aggregation + " aggregation")
	}
	return span, nil
}

// newRunningTolerance maps the one period a single-day read answers with.
//
// Garmin answers with a list, and the read asks for one day, so any period past the
// first is drift rather than data.
func newRunningTolerance(
	date client.Date, entries []api.RunningToleranceEntry,
) RunningTolerance {
	out := RunningTolerance{Date: date.String()}
	if len(entries) == 0 {
		out.Note = "the account's device does not report running tolerance for this day"
		return out
	}

	point := newRunningTolerancePoint(entries[0])
	out.Supported = true
	if point.Date != nil {
		out.Date = *point.Date
	}
	out.ToleranceKM = point.ToleranceKM
	out.AcuteLoadKM = point.AcuteLoadKM
	out.DistanceKM = point.DistanceKM
	out.LoadRatio = point.LoadRatio
	out.FeedbackPhrase = point.FeedbackPhrase
	return out
}

// newRunningToleranceTrend maps the window onto the bounded result, oldest period
// first: Garmin does not return the daily aggregation in chronological order.
func newRunningToleranceTrend(
	span client.DateRange, aggregation string, entries []api.RunningToleranceEntry,
) RunningToleranceTrend {
	out := RunningToleranceTrend{
		StartDate:   span.Start().String(),
		EndDate:     span.End().String(),
		Aggregation: aggregation,
		Trend:       []RunningTolerancePoint{},
	}
	if len(entries) == 0 {
		out.Note = "the account's device does not report running tolerance for this window"
		return out
	}

	out.Supported = true
	for _, entry := range entries {
		out.Trend = append(out.Trend, newRunningTolerancePoint(entry))
	}
	slices.SortStableFunc(out.Trend, func(a, b RunningTolerancePoint) int {
		return compareOptionalDates(a.Date, b.Date)
	})
	out.DataPoints = len(out.Trend)

	first, latest := firstToleranceKM(out.Trend), latestToleranceKM(out.Trend)
	out.FirstToleranceKM, out.LatestToleranceKM = first, latest
	if first != nil && latest != nil {
		out.ToleranceChangeKM = new(roundToTwoDecimals(*latest - *first))
	}
	return out
}

// newRunningTolerancePoint curates one period, converting the metres Garmin reports
// into the kilometres this tool reports.
func newRunningTolerancePoint(entry api.RunningToleranceEntry) RunningTolerancePoint {
	point := RunningTolerancePoint{
		Date:           entry.CalendarDate,
		ToleranceKM:    kilometresFrom(entry.ToleranceMeters()),
		AcuteLoadKM:    kilometresFrom(entry.ImpactLoadMeters()),
		DistanceKM:     kilometresFrom(entry.DistanceMeters()),
		FeedbackPhrase: optionalText(entry.FeedbackPhrase),
		StartOfWeek:    entry.StartOfWeek,
		EndOfWeek:      entry.EndOfWeek,
		WeekIndex:      optionalInt(entry.WeekIndex),
	}

	load, hasLoad := entry.ImpactLoadMeters()
	distance, hasDistance := entry.DistanceMeters()
	if hasLoad && hasDistance && distance != 0 {
		point.LoadRatio = new(roundToTwoDecimals(load / distance))
	}
	return point
}

// kilometresFrom converts a metre reading into kilometres, rounded the way upstream
// rounds it. An unset reading produces no value.
func kilometresFrom(meters float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return new(roundToTwoDecimals(meters / 1000))
}

// compareOptionalDates orders two optional calendar days, an absent day last.
func compareOptionalDates(a, b *string) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	default:
		return strings.Compare(*a, *b)
	}
}

// firstToleranceKM is the earliest period that carries a capacity.
func firstToleranceKM(points []RunningTolerancePoint) *float64 {
	for _, point := range points {
		if point.ToleranceKM != nil {
			return point.ToleranceKM
		}
	}
	return nil
}

// latestToleranceKM is the most recent period that carries a capacity.
func latestToleranceKM(points []RunningTolerancePoint) *float64 {
	for _, point := range slices.Backward(points) {
		if point.ToleranceKM != nil {
			return point.ToleranceKM
		}
	}
	return nil
}
