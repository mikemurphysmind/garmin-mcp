package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tamcore/garmin-mcp/internal/garmin/client"
	"github.com/tamcore/garmin-mcp/internal/testkit"
)

// The two sport keys the zone tests name.
const (
	zoneSportRunning = "RUNNING"
	zoneSportCycling = "cycling"
)

// Every zone fixture here is synthetic: no rate, floor or method comes from a real
// account.
const zonesBody = `[` +
	`{"sport":"DEFAULT","maxHeartRateUsed":190,"restingHeartRateUsed":52,` +
	`"restingHrAutoUpdateUsed":true,"lactateThresholdHeartRateUsed":170,` +
	`"trainingMethod":"HR_MAX","zone1Floor":95,"zone2Floor":114,"zone3Floor":133,` +
	`"zone4Floor":152,"zone5Floor":171},` +
	`{"sport":"` + zoneSportRunning + `","maxHeartRateUsed":193,"trainingMethod":"HR_RESERVE",` +
	`"zone1Floor":97,"zone2Floor":116,"zone3Floor":135,"zone4Floor":154,` +
	`"zone5Floor":173}]`

// zonesScript scripts the one endpoint both tools use. The behaviours are queued, so a
// write test can serve a changed document on the read-back.
func zonesScript(behaviors ...testkit.Behavior) testkit.Script {
	if len(behaviors) == 0 {
		behaviors = []testkit.Behavior{testkit.JSON(http.StatusOK, zonesBody)}
	}
	return testkit.NewScript().With(client.PathHeartRateZones, behaviors...)
}

// writeThenReadBack is the queue a successful write sees: the current profiles, the
// 204 Garmin answers the write with, then the profiles it saved.
func writeThenReadBack(savedBody string) testkit.Script {
	return zonesScript(
		testkit.JSON(http.StatusOK, zonesBody),
		testkit.Behavior{Status: http.StatusNoContent},
		testkit.JSON(http.StatusOK, savedBody),
	)
}

func TestGetHeartRateZonesReturnsEverySavedProfile(t *testing.T) {
	t.Parallel()

	h := newToolHarness(t, zonesScript())

	result := h.call(t, ToolGetHeartRateZones, nil)

	zones := list(t, result, "zones")
	if len(zones) != 2 {
		t.Fatalf("zones holds %d profiles, want both saved profiles", len(zones))
	}
	if got := number(t, result, "count"); got != 2 {
		t.Errorf("count = %v, want 2", got)
	}

	first := entry(t, zones, 0)
	if got, _ := first["sport"].(string); got != "DEFAULT" {
		t.Errorf("zones[0].sport = %q, want DEFAULT", got)
	}
	if got := number(t, first, "max_hr_bpm"); got != 190 {
		t.Errorf("zones[0].max_hr_bpm = %v, want 190", got)
	}
	floors := list(t, first, "zone_boundaries_bpm")
	if len(floors) != 5 {
		t.Fatalf("zones[0].zone_boundaries_bpm holds %d floors, want five", len(floors))
	}
	if got, _ := floors[0].(float64); got != 95 {
		t.Errorf("the first floor = %v, want 95", got)
	}
}

// TestGetHeartRateZonesFiltersBySport proves a named sport narrows the result, and that
// generic is accepted for Garmin's DEFAULT.
func TestGetHeartRateZonesFiltersBySport(t *testing.T) {
	t.Parallel()

	for name, expected := range map[string]string{
		defaultCourseActivityType: zoneSportRunning,
		"generic":                 "DEFAULT",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newToolHarness(t, zonesScript())

			result := h.call(t, ToolGetHeartRateZones, map[string]any{argSport: name})

			zones := list(t, result, "zones")
			if len(zones) != 1 {
				t.Fatalf("zones holds %d profiles, want just the named sport", len(zones))
			}
			if got, _ := entry(t, zones, 0)["sport"].(string); got != expected {
				t.Errorf("zones[0].sport = %q, want %q", got, expected)
			}
		})
	}
}

// TestGetHeartRateZonesReportsASportWithNoProfile proves an unconfigured sport answers
// with an empty list and a note rather than a failure.
func TestGetHeartRateZonesReportsASportWithNoProfile(t *testing.T) {
	t.Parallel()

	h := newToolHarness(t, zonesScript())

	result := h.call(t, ToolGetHeartRateZones, map[string]any{argSport: "swimming"})

	if got := len(list(t, result, "zones")); got != 0 {
		t.Errorf("zones holds %d profiles, want none", got)
	}
	if note, _ := result["note"].(string); note == "" {
		t.Error("no note explains the absent profile")
	}
}

// TestGetHeartRateZonesRefusesAMalformedSport keeps validation at the boundary.
func TestGetHeartRateZonesRefusesAMalformedSport(t *testing.T) {
	t.Parallel()

	h := newToolHarness(t, zonesScript())

	if err := h.callError(t, ToolGetHeartRateZones,
		map[string]any{argSport: stressTraversal}); err == "" {
		t.Error("a malformed sport key was accepted")
	}
	if got := len(h.fake.Requests()); got != 0 {
		t.Errorf("dispatched %d requests for a refused argument, want none", got)
	}
}

// TestSetHeartRateZonesMergesOntoTheCurrentProfile proves the write is a
// read-modify-write: an omitted field keeps the value Garmin already holds, and the
// result is the profile Garmin reported on read-back.
func TestSetHeartRateZonesMergesOntoTheCurrentProfile(t *testing.T) {
	t.Parallel()

	savedBody := `[{"sport":"` + zoneSportRunning + `","maxHeartRateUsed":196,` +
		`"trainingMethod":"HR_RESERVE","zone1Floor":97,"zone2Floor":116,` +
		`"zone3Floor":135,"zone4Floor":154,"zone5Floor":173}]`
	svc, fake := cardioService(t, writeThenReadBack(savedBody))

	out, err := svc.setHeartRateZones(cardioContext(t), setHeartRateZonesInput{
		Sport: defaultCourseActivityType, MaxHR: new(196),
	})
	if err != nil {
		t.Fatalf("setHeartRateZones() = %v", err)
	}

	if !out.Applied {
		t.Fatalf("Applied = false, want the saved profile reported: %+v", out)
	}
	if out.Zone.MaxHeartRateBPM == nil || *out.Zone.MaxHeartRateBPM != 196 {
		t.Errorf("the saved max rate = %v, want 196", out.Zone.MaxHeartRateBPM)
	}

	body := writtenZonePayload(t, fake)
	if got := body["maxHeartRateUsed"]; got != float64(196) {
		t.Errorf("the write sent maxHeartRateUsed = %v, want 196", got)
	}
	// The untouched fields are sent back unchanged rather than as zeroes.
	if got := body["zone3Floor"]; got != float64(135) {
		t.Errorf("the write sent zone3Floor = %v, want the current 135", got)
	}
	if got := body["trainingMethod"]; got != "HR_RESERVE" {
		t.Errorf("the write sent trainingMethod = %v, want the current HR_RESERVE", got)
	}
	if got := body["changeState"]; got != "CHANGED" {
		t.Errorf("the write sent changeState = %v, want CHANGED", got)
	}
	if got := body["sport"]; got != zoneSportRunning {
		t.Errorf("the write sent sport = %v, want the normalized RUNNING", got)
	}
}

// TestSetHeartRateZonesInheritsTheDefaultProfile proves a sport Garmin holds no
// profile for is written from DEFAULT's values under its own sport key.
func TestSetHeartRateZonesInheritsTheDefaultProfile(t *testing.T) {
	t.Parallel()

	svc, fake := cardioService(t, writeThenReadBack(zonesBody))

	_, err := svc.setHeartRateZones(cardioContext(t), setHeartRateZonesInput{
		Sport: zoneSportCycling, MaxHR: new(185),
	})
	if err != nil {
		t.Fatalf("setHeartRateZones() = %v", err)
	}

	body := writtenZonePayload(t, fake)
	if got := body["sport"]; got != strings.ToUpper(zoneSportCycling) {
		t.Errorf("the write sent sport = %v, want the normalized CYCLING", got)
	}
	// DEFAULT's floors are inherited; only the supplied maximum differs.
	if got := body["zone1Floor"]; got != float64(95) {
		t.Errorf("the write sent zone1Floor = %v, want DEFAULT's 95", got)
	}
	if got := body["maxHeartRateUsed"]; got != float64(185) {
		t.Errorf("the write sent maxHeartRateUsed = %v, want 185", got)
	}
}

// TestSetHeartRateZonesStopsTheRestingAutoUpdate proves supplying a resting rate turns
// Garmin's own updating off, which is what upstream does.
func TestSetHeartRateZonesStopsTheRestingAutoUpdate(t *testing.T) {
	t.Parallel()

	svc, fake := cardioService(t, writeThenReadBack(zonesBody))

	_, err := svc.setHeartRateZones(cardioContext(t), setHeartRateZonesInput{
		Sport: "default", RestingHR: new(48),
	})
	if err != nil {
		t.Fatalf("setHeartRateZones() = %v", err)
	}

	body := writtenZonePayload(t, fake)
	if got := body["restingHeartRateUsed"]; got != float64(48) {
		t.Errorf("the write sent restingHeartRateUsed = %v, want 48", got)
	}
	if got := body["restingHrAutoUpdateUsed"]; got != false {
		t.Errorf("the write sent restingHrAutoUpdateUsed = %v, want false", got)
	}
}

// TestSetHeartRateZonesSendsCustomFloorsAsHRMax proves the custom sentinel never
// reaches Garmin: it stores no such method, so HR_MAX is sent with explicit floors.
func TestSetHeartRateZonesSendsCustomFloorsAsHRMax(t *testing.T) {
	t.Parallel()

	svc, fake := cardioService(t, writeThenReadBack(zonesBody))

	_, err := svc.setHeartRateZones(cardioContext(t), setHeartRateZonesInput{
		Sport: "default", CalculationMethod: new("custom_bpm"),
		ZoneBoundaries: []int{100, 120, 140, 160, 180},
	})
	if err != nil {
		t.Fatalf("setHeartRateZones() = %v", err)
	}

	body := writtenZonePayload(t, fake)
	if got := body["trainingMethod"]; got != "HR_MAX" {
		t.Errorf("the write sent trainingMethod = %v, want HR_MAX", got)
	}
	for field, want := range map[string]float64{
		"zone1Floor": 100, "zone3Floor": 140, "zone5Floor": 180,
	} {
		if got := body[field]; got != want {
			t.Errorf("the write sent %s = %v, want %v", field, got, want)
		}
	}
}

// TestSetHeartRateZonesRefusesInconsistentArguments keeps every rule at the boundary or
// at the merge, and proves nothing is written when one is broken.
func TestSetHeartRateZonesRefusesInconsistentArguments(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]setHeartRateZonesInput{
		"nothing to change": {Sport: defaultCourseActivityType},
		"floors without the custom method": {
			Sport: defaultCourseActivityType, ZoneBoundaries: []int{100, 120, 140, 160, 180},
		},
		"custom method without floors": {
			Sport: defaultCourseActivityType, CalculationMethod: new("custom_bpm"),
		},
		"floors that do not increase": {
			Sport: defaultCourseActivityType, CalculationMethod: new("custom_bpm"),
			ZoneBoundaries: []int{100, 120, 120, 160, 180},
		},
		"resting at or above the maximum": {
			Sport: defaultCourseActivityType, RestingHR: new(200), MaxHR: new(190),
		},
		"threshold above the maximum": {
			Sport: defaultCourseActivityType, LactateThresholdHR: new(195), MaxHR: new(190),
		},
		"floors above the maximum": {
			Sport: defaultCourseActivityType, MaxHR: new(150), CalculationMethod: new("custom_bpm"),
			ZoneBoundaries: []int{100, 120, 140, 160, 180},
		},
		"unknown method":  {Sport: defaultCourseActivityType, CalculationMethod: new("guesswork")},
		"malformed sport": {Sport: "../etc", MaxHR: new(190)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc, fake := cardioService(t, zonesScript())

			if _, err := svc.setHeartRateZones(cardioContext(t), in); err == nil {
				t.Fatalf("%s was accepted", name)
			}
			for _, request := range fake.Requests() {
				if request.Method != http.MethodGet {
					t.Errorf("%s dispatched a %s, want no write", name, request.Method)
				}
			}
		})
	}
}

// TestHeartRateZoneLogValuesReportShapeOnly keeps a physiological reading out of the
// logs.
func TestHeartRateZoneLogValuesReportShapeOnly(t *testing.T) {
	t.Parallel()

	zone := HeartRateZoneConfig{Sport: zoneSportRunning, MaxHeartRateBPM: new(193)}
	zones := HeartRateZoneList{Zones: []HeartRateZoneConfig{zone}, Count: 1}
	update := HeartRateZoneUpdate{Sport: zoneSportRunning, Applied: true, Zone: zone}

	for _, rendered := range []string{zones.LogValue().String(), update.LogValue().String()} {
		if strings.Contains(rendered, "193") {
			t.Errorf("the log value %q carries a reading", rendered)
		}
	}
}

// writtenZonePayload decodes the single profile the write sent.
func writtenZonePayload(t *testing.T, fake *testkit.Server) map[string]any {
	t.Helper()

	for _, request := range fake.Requests() {
		if request.Method != http.MethodPut {
			continue
		}
		var body []map[string]any
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatalf("decoding the write body: %v", err)
		}
		if len(body) != 1 {
			t.Fatalf("the write sent %d profiles, want exactly one", len(body))
		}
		return body[0]
	}
	t.Fatal("no write was dispatched")
	return nil
}
