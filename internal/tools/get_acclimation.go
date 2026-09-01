package tools

import (
	"context"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tamcore/garmin-mcp/internal/garmin/api"
	"github.com/tamcore/garmin-mcp/internal/garmin/client"
	"github.com/tamcore/garmin-mcp/internal/mcpserver"
	"github.com/tamcore/garmin-mcp/internal/policy"
)

// ToolGetAcclimation is the upstream compatibility name of the acclimation read. It
// is an addition beyond the pinned manifest: upstream added it after the pin.
const ToolGetAcclimation = "get_acclimation"

// Acclimation is one day's heat-and-altitude acclimation state.
//
// It is health data — never log it, never cache it. Garmin populates the section only
// after outdoor activities in heat or at altitude, so an account with none reports
// available=false rather than a day of zeroes.
type Acclimation struct {
	Date      string `json:"date" jsonschema:"the day the reading is dated, YYYY-MM-DD"`
	Available bool   `json:"available" jsonschema:"whether Garmin holds a reading"`

	HeatPercent         *float64 `json:"heat_acclimation_percent,omitempty" jsonschema:"heat adaptation, 0-100"`
	PreviousHeatPercent *float64 `json:"previous_heat_acclimation_percent,omitempty" jsonschema:"the prior reading"`
	HeatChange          *float64 `json:"heat_acclimation_change,omitempty" jsonschema:"current minus prior"`
	HeatTrend           *string  `json:"heat_trend,omitempty" jsonschema:"Garmin's heat label"`
	HeatDate            *string  `json:"heat_acclimation_date,omitempty" jsonschema:"the reading's day"`
	PreviousHeatDate    *string  `json:"previous_heat_acclimation_date,omitempty" jsonschema:"the prior day"`

	AltitudeMeters         *float64 `json:"altitude_acclimation_meters,omitempty" jsonschema:"adapted altitude"`
	PreviousAltitudeMeters *float64 `json:"previous_altitude_acclimation_meters,omitempty" jsonschema:"prior altitude"`
	AltitudeTrend          *string  `json:"altitude_trend,omitempty" jsonschema:"Garmin's altitude label"`
	CurrentAltitudeMeters  *float64 `json:"current_altitude_meters,omitempty" jsonschema:"last recorded altitude"`

	Note string `json:"note,omitempty" jsonschema:"why no reading is reported"`
}

// LogValue reports whether a reading exists, never the reading.
func (a Acclimation) LogValue() slog.Value {
	return shape("acclimation", slog.Bool("available", a.Available))
}

// acclimationInput is the strict argument set: one calendar day.
type acclimationInput struct {
	Date string `json:"date" jsonschema:"the calendar day to read, YYYY-MM-DD"`
}

func getAcclimationContract() Contract {
	return Contract{
		Spec: mcpserver.ToolSpec{
			Name:  ToolGetAcclimation,
			Title: "Get the heat and altitude acclimation",
			Description: "read how adapted the account is to training in heat and at " +
				"altitude on one day. The heat figure runs from 0 to 100 and decays " +
				"without continued exposure, and the previous reading is returned beside " +
				"it so the direction of travel is visible in one call. Garmin populates " +
				"this only after outdoor activities in heat or at altitude, so an account " +
				"with none reports available=false. VO2 max is not part of this read; use " +
				"get_training_status or get_vo2max_trend",
			Tier:        policy.TierReadOnly,
			Category:    categoryHealth,
			Annotations: readOnlyAnnotations(),
		},
		Schema: NewSchema(dateProperty("date", "the calendar day to read")),
	}
}

// registerGetAcclimation registers the tool.
func registerGetAcclimation(registry *mcpserver.Registry, svc *service) error {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in acclimationInput) (
		*mcp.CallToolResult, Acclimation, error,
	) {
		date, err := parseCalendarDate("date", in.Date)
		if err != nil {
			return nil, Acclimation{}, err
		}
		session, err := svc.session(ctx)
		if err != nil {
			return nil, Acclimation{}, err
		}

		metrics, err := svc.trends().Acclimation(ctx, session, date)
		if err != nil {
			return nil, Acclimation{}, fail(err)
		}
		return nil, newAcclimation(date, metrics), nil
	}
	return mcpserver.AddTool(registry, getAcclimationContract().Registration(), handler)
}

// newAcclimation maps the first section the document carries onto the bounded result.
//
// Garmin answers this endpoint with a list, and the read asks for one day, so any day
// past the first is drift rather than data.
func newAcclimation(date client.Date, metrics api.MaxMetrics) Acclimation {
	out := Acclimation{Date: date.String()}

	section := firstAcclimationSection(metrics)
	if section == nil {
		out.Note = "Garmin holds no acclimation reading for this day. It populates " +
			"this only after outdoor activities in heat or at altitude."
		return out
	}

	out.Available = true
	if section.CalendarDate != nil && *section.CalendarDate != "" {
		out.Date = *section.CalendarDate
	}
	out.HeatPercent = optionalFloat(section.HeatAcclimationPercentage)
	out.PreviousHeatPercent = optionalFloat(section.PreviousHeatAcclimation)
	out.HeatTrend = optionalText(section.HeatTrend)
	out.HeatDate = section.HeatAcclimationDate
	out.PreviousHeatDate = section.PreviousHeatAcclimationDate
	out.AltitudeMeters = optionalFloat(section.AltitudeAcclimation)
	out.PreviousAltitudeMeters = optionalFloat(section.PreviousAltitudeAcclimation)
	out.AltitudeTrend = optionalText(section.AltitudeTrend)
	out.CurrentAltitudeMeters = optionalFloat(section.CurrentAltitude)

	if heat, prior := out.HeatPercent, out.PreviousHeatPercent; heat != nil && prior != nil {
		out.HeatChange = new(roundToOneDecimal(*heat - *prior))
	}
	return out
}

// firstAcclimationSection returns the first acclimation section the document carries.
func firstAcclimationSection(metrics api.MaxMetrics) *api.Acclimation {
	for _, day := range metrics.Days() {
		if day.Acclimation != nil {
			return day.Acclimation
		}
	}
	return nil
}
