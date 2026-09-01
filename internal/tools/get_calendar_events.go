package tools

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tamcore/garmin-mcp/internal/garmin/api"
	"github.com/tamcore/garmin-mcp/internal/garmin/client"
	"github.com/tamcore/garmin-mcp/internal/mcpserver"
	"github.com/tamcore/garmin-mcp/internal/policy"
)

// ToolGetCalendarEvents is the upstream compatibility name of the calendar-event read.
// It is an addition beyond the pinned manifest: upstream added it after the pinned
// commit.
const ToolGetCalendarEvents = "get_calendar_events"

// DefaultMaxCalendarEvents bounds a returned event listing. The window is bounded too,
// but a single month can hold many subscribed events, so the entry count needs a
// ceiling of its own.
const DefaultMaxCalendarEvents = 500

// A CalendarEvent is one race or event on the account's calendar.
//
// It is personal material: it says where a person intends to be on a given day. The
// field set is upstream's curated shape, so a client that already reads garmin_mcp
// finds the same keys.
type CalendarEvent struct {
	Title *string `json:"title,omitempty" jsonschema:"the event's name"`
	Date  *string `json:"date,omitempty" jsonschema:"the calendar day, YYYY-MM-DD"`

	IsRace       bool `json:"is_race" jsonschema:"whether Garmin marks the entry a race"`
	PrimaryEvent bool `json:"primary_event" jsonschema:"whether a plan targets it"`
	Subscribed   bool `json:"subscribed" jsonschema:"whether the account subscribed"`

	DistanceMeters *float64 `json:"distance_meters,omitempty" jsonschema:"the distance goal"`
	StartTimeLocal *string  `json:"start_time_local,omitempty" jsonschema:"local start, HH:MM"`
	TimeZone       *string  `json:"time_zone,omitempty" jsonschema:"the event's time zone"`

	Location  *string `json:"location,omitempty" jsonschema:"the published location"`
	URL       *string `json:"url,omitempty" jsonschema:"the event's own link"`
	EventUUID *string `json:"event_uuid,omitempty" jsonschema:"Garmin's shareable id"`
}

// A CalendarEventList is the bounded event listing.
type CalendarEventList struct {
	Events    []CalendarEvent `json:"events" jsonschema:"the events, by date then title"`
	Count     int             `json:"count" jsonschema:"how many events this result carries"`
	StartDate string          `json:"start_date" jsonschema:"the inclusive first day read"`
	EndDate   string          `json:"end_date" jsonschema:"the inclusive last day read"`
	Truncated bool            `json:"truncated" jsonschema:"whether the listing was cut"`
}

// LogValue reports the listing size, never an event.
func (l CalendarEventList) LogValue() slog.Value {
	return shape("calendarEventList",
		slog.Int("events", len(l.Events)),
		slog.Bool("truncated", l.Truncated),
	)
}

// calendarEventsInput is the strict argument set: an inclusive date window.
type calendarEventsInput struct {
	StartDate string `json:"start_date" jsonschema:"the inclusive first day, YYYY-MM-DD"`
	EndDate   string `json:"end_date" jsonschema:"the inclusive last day, YYYY-MM-DD"`
}

func getCalendarEventsContract() Contract {
	return Contract{
		Spec: mcpserver.ToolSpec{
			Name:  ToolGetCalendarEvents,
			Title: "Get the calendar events",
			Description: "read the races and events on the Garmin Connect calendar between " +
				"two dates, inclusive. These are the entries the account added or " +
				"subscribed to; get_scheduled_workouts covers workouts and get_goals " +
				"covers goals, and neither returns these. Each event reports its distance " +
				"goal when Garmin stores one, the local start time the organiser " +
				"published, whether Garmin marks it a race, and whether an active training " +
				"plan targets it. Garmin serves this calendar a month at a time, so a " +
				"window costs one read per month it spans",
			Tier:        policy.TierReadOnly,
			Category:    categoryLocation,
			Annotations: readOnlyAnnotations(),
		},
		Schema: NewSchema(
			dateProperty("start_date", "inclusive first day of the window"),
			dateProperty("end_date", "inclusive last day of the window"),
		),
	}
}

// registerGetCalendarEvents registers the tool.
func registerGetCalendarEvents(registry *mcpserver.Registry, svc *service) error {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in calendarEventsInput) (
		*mcp.CallToolResult, CalendarEventList, error,
	) {
		span, err := parseWindow(in.StartDate, in.EndDate, svc.limits)
		if err != nil {
			return nil, CalendarEventList{}, err
		}
		session, err := svc.session(ctx)
		if err != nil {
			return nil, CalendarEventList{}, err
		}

		items, err := svc.readCalendarMonths(ctx, session, span)
		if err != nil {
			return nil, CalendarEventList{}, err
		}
		return nil, newCalendarEventList(span, items), nil
	}
	return mcpserver.AddTool(registry, getCalendarEventsContract().Registration(), handler)
}

// readCalendarMonths reads every month the window touches.
//
// Garmin serves this calendar a month at a time, and the first and last month extend
// past the window, so the months are read whole and the window is applied to the
// entries afterwards.
func (s *service) readCalendarMonths(
	ctx context.Context, session client.Session, span client.DateRange,
) ([]api.CalendarItem, error) {
	var items []api.CalendarItem
	for _, month := range monthsSpanning(span) {
		read, err := s.calendar.MonthItems(ctx, session, month.year, month.month)
		if err != nil {
			return nil, fail(err)
		}
		items = append(items, read...)
	}
	return items, nil
}

// A spannedMonth is one year-and-month pair a window touches.
type spannedMonth struct {
	year  int
	month int
}

// monthsSpanning lists the months the window touches, oldest first. The window is
// bounded before this runs, so the list is bounded with it.
func monthsSpanning(span client.DateRange) []spannedMonth {
	start, end := span.Start().Time(), span.End().Time()
	var months []spannedMonth
	for year, month := start.Year(), int(start.Month()); ; {
		months = append(months, spannedMonth{year: year, month: month})
		if year == end.Year() && month == int(end.Month()) {
			return months
		}
		if month == 12 {
			year, month = year+1, 1
			continue
		}
		month++
	}
}

// newCalendarEventList keeps the events inside the window, drops the duplicates a
// multi-month read can produce at the seams, and orders and bounds the result.
func newCalendarEventList(
	span client.DateRange, items []api.CalendarItem,
) CalendarEventList {
	start, end := span.Start().String(), span.End().String()
	out := CalendarEventList{StartDate: start, EndDate: end, Events: []CalendarEvent{}}

	type seenKey struct{ uuid, title, date string }
	seen := make(map[seenKey]struct{}, len(items))
	for _, item := range items {
		if !item.IsEvent() || item.Date == nil {
			continue
		}
		// The first and last month of the window extend past it.
		if *item.Date < start || *item.Date > end {
			continue
		}
		key := seenKey{
			uuid:  stringOrEmpty(item.EventUUID),
			title: stringOrEmpty(item.Title),
			date:  *item.Date,
		}
		if _, repeated := seen[key]; repeated {
			continue
		}
		seen[key] = struct{}{}
		out.Events = append(out.Events, newCalendarEvent(item))
	}

	slices.SortStableFunc(out.Events, compareCalendarEvents)
	if len(out.Events) > DefaultMaxCalendarEvents {
		out.Events = out.Events[:DefaultMaxCalendarEvents]
		out.Truncated = true
	}
	out.Count = len(out.Events)
	return out
}

// compareCalendarEvents orders by date, then by title, exactly as upstream sorts.
func compareCalendarEvents(a, b CalendarEvent) int {
	if ordered := compareOptionalDates(a.Date, b.Date); ordered != 0 {
		return ordered
	}
	return strings.Compare(stringOrEmpty(a.Title), stringOrEmpty(b.Title))
}

// newCalendarEvent curates one calendar item.
func newCalendarEvent(item api.CalendarItem) CalendarEvent {
	event := CalendarEvent{
		Title:        item.Title,
		Date:         item.Date,
		IsRace:       isFlagged(item.IsRace),
		PrimaryEvent: isFlagged(item.PrimaryEvent),
		Subscribed:   isFlagged(item.Subscribed),
		Location:     trimmedOrNil(item.Location),
		URL:          item.URL,
		EventUUID:    item.EventUUID,
	}
	if distance, ok := item.DistanceMeters(); ok {
		event.DistanceMeters = &distance
	}
	if local := item.EventTime; local != nil {
		event.StartTimeLocal = local.StartTimeHhMm
		event.TimeZone = local.TimeZoneID
	}
	return event
}

// trimmedOrNil renders an optional string without its surrounding space, and reports
// the blanks Garmin sends as no value at all. Source: _clean_str.
func trimmedOrNil(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
