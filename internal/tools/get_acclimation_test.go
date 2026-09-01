package tools

import (
	"net/http"
	"testing"

	"github.com/tamcore/garmin-mcp/internal/garmin/client"
	"github.com/tamcore/garmin-mcp/internal/testkit"
)

// The acclimation fixtures are synthetic: no reading here comes from a real account.
const acclimationDate = "2026-02-01"

// acclimationBody is the shape Garmin answers the one-day max-metrics read with. The
// current percentage arrives as a number and the previous one as a numeric string,
// both of which the union decoder reads.
const acclimationBody = `[{"generic":{"vo2MaxValue":52.0},` +
	`"heatAltitudeAcclimation":{"calendarDate":"` + acclimationDate + `",` +
	`"heatAcclimationPercentage":78,"previousHeatAcclimationPercentage":"71.5",` +
	`"heatTrend":"ACCLIMATIZED","heatAcclimationDate":"` + acclimationDate + `",` +
	`"previousHeatAcclimationDate":"2026-01-28","altitudeAcclimation":1450,` +
	`"previousAltitudeAcclimation":1200,"altitudeTrend":"ACCLIMATIZING",` +
	`"currentAltitude":412,"userProfilePK":900001}}]`

// acclimationPath is the one-day path: the day is both segments.
func acclimationPath(day string) string {
	return client.PathMaxMetricsPrefix + "/" + day + "/" + day
}

func acclimationScript(body string) testkit.Script {
	return testkit.NewScript().With(acclimationPath(acclimationDate),
		testkit.JSON(http.StatusOK, body))
}

func TestGetAcclimationReturnsTheHeatAndAltitudeState(t *testing.T) {
	t.Parallel()

	h := newToolHarness(t, acclimationScript(acclimationBody))

	result := h.call(t, ToolGetAcclimation, map[string]any{argDate: acclimationDate})

	if available, _ := result["available"].(bool); !available {
		t.Fatalf("available = false, want true: %v", result)
	}
	if got, _ := result["date"].(string); got != acclimationDate {
		t.Errorf("date = %q, want %q", got, acclimationDate)
	}
	if got := number(t, result, "heat_acclimation_percent"); got != 78 {
		t.Errorf("heat_acclimation_percent = %v, want 78", got)
	}
	// The prior reading arrives as a numeric string and still decodes.
	if got := number(t, result, "previous_heat_acclimation_percent"); got != 71.5 {
		t.Errorf("previous_heat_acclimation_percent = %v, want 71.5", got)
	}
	if got := number(t, result, "heat_acclimation_change"); got != 6.5 {
		t.Errorf("heat_acclimation_change = %v, want 6.5", got)
	}
	if got, _ := result["heat_trend"].(string); got != "ACCLIMATIZED" {
		t.Errorf("heat_trend = %q, want Garmin's own label", got)
	}
	if got := number(t, result, "altitude_acclimation_meters"); got != 1450 {
		t.Errorf("altitude_acclimation_meters = %v, want 1450", got)
	}
	if got := number(t, result, "current_altitude_meters"); got != 412 {
		t.Errorf("current_altitude_meters = %v, want 412", got)
	}
}

// TestGetAcclimationReportsAnAccountWithNoReading proves an account Garmin holds no
// section for is reported as such rather than as a day of zeroes.
func TestGetAcclimationReportsAnAccountWithNoReading(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"no section":  `[{"generic":{"vo2MaxValue":52.0}}]`,
		"empty array": `[]`,
		"null":        jsonNull,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newToolHarness(t, acclimationScript(body))

			result := h.call(t, ToolGetAcclimation, map[string]any{argDate: acclimationDate})

			if available, _ := result["available"].(bool); available {
				t.Errorf("available = true, want false for %s", name)
			}
			if note, _ := result["note"].(string); note == "" {
				t.Error("no note explains the absent reading")
			}
			if _, present := result["heat_acclimation_percent"]; present {
				t.Error("a reading was reported for an account that holds none")
			}
		})
	}
}

// TestGetAcclimationRefusesAMalformedDate keeps the argument validation at the
// boundary, before any request is dispatched.
func TestGetAcclimationRefusesAMalformedDate(t *testing.T) {
	t.Parallel()

	h := newToolHarness(t, acclimationScript(acclimationBody))

	if err := h.callError(t, ToolGetAcclimation, map[string]any{argDate: "01-02-2026"}); err == "" {
		t.Error("a malformed date was accepted")
	}
	if got := len(h.fake.Requests()); got != 0 {
		t.Errorf("dispatched %d requests for a refused argument, want none", got)
	}
}

// TestGetAcclimationCarriesNoAccountKey proves the account identifier Garmin sends
// inside the section never reaches the caller.
func TestGetAcclimationCarriesNoAccountKey(t *testing.T) {
	t.Parallel()

	h := newToolHarness(t, acclimationScript(acclimationBody))

	rendered := h.text(t, ToolGetAcclimation, map[string]any{argDate: acclimationDate})

	if contains(rendered, "900001") {
		t.Error("the account key reached the caller")
	}
}

// TestAcclimationLogValueReportsShapeOnly keeps a health reading out of the logs.
func TestAcclimationLogValueReportsShapeOnly(t *testing.T) {
	t.Parallel()

	heat := 78.0
	out := Acclimation{Date: acclimationDate, Available: true, HeatPercent: &heat}

	rendered := out.LogValue().String()
	if contains(rendered, "78") {
		t.Errorf("the log value %q carries the reading", rendered)
	}
	if !contains(rendered, "acclimation") {
		t.Errorf("the log value %q does not name the model", rendered)
	}
}
