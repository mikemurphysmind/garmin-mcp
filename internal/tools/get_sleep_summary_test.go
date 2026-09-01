package tools

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tamcore/garmin-mcp/internal/garmin/client"
	"github.com/tamcore/garmin-mcp/internal/testkit"
)

const cardioSleepBody = `{"dailySleepDTO":{"calendarDate":"` + cardioDate + `",` +
	`"sleepTimeSeconds":27000,"napTimeSeconds":0,"deepSleepSeconds":5400,` +
	`"lightSleepSeconds":16200,"remSleepSeconds":5400,"awakeSleepSeconds":600,` +
	`"awakeCount":1,"restlessMomentsCount":9,"avgSleepStress":17.0,"restingHeartRate":52,` +
	`"sleepStartTimestampGMT":1786689600000,"sleepEndTimestampGMT":1786716600000,` +
	`"sleepScores":{"overall":{"value":81,"qualifierKey":"GOOD"}}},` +
	`"wellnessSpO2SleepSummaryDTO":{"averageSpo2":96,"lowestSpo2":92},"avgOvernightHrv":48.0}`

func cardioSleepPath() string {
	return client.PathDailySleepPrefix + "/" + cardioDisplayName
}

func TestReadSleepSummaryReturnsBothViewsOfTheOneDocument(t *testing.T) {
	t.Parallel()

	svc, fake := cardioService(t, cardioScript(cardioJSON(cardioSleepPath(), cardioSleepBody)))

	got, err := svc.readSleepSummary(cardioContext(t), cardioDate)
	if err != nil {
		t.Fatalf("readSleepSummary() = %v", err)
	}

	if !got.HasData {
		t.Error("HasData = false, want true")
	}
	if got.SleepScoreQualifier == nil || *got.SleepScoreQualifier != "GOOD" {
		t.Errorf("SleepScoreQualifier = %v, want GOOD", got.SleepScoreQualifier)
	}
	for _, field := range []struct {
		name string
		got  *float64
		want float64
	}{
		{"SleepSeconds", got.SleepSeconds, 27000},
		{"SleepScore", got.SleepScore, 81},
		{"RestlessMomentsCount", got.RestlessMomentsCount, 9},
		{"AvgSpO2Percent", got.AvgSpO2Percent, 96},
		{"AvgOvernightHRV", got.AvgOvernightHRV, 48},
	} {
		if field.got == nil || *field.got != field.want {
			t.Errorf("%s = %v, want %v", field.name, field.got, field.want)
		}
	}

	if calls := len(fake.Requests()); calls != 2 {
		t.Errorf("requests = %d, want 2: one profile read and one sleep read, "+
			"since the digest re-reads the retained payload", calls)
	}
}

func TestReadSleepSummaryReportsANightWithNoData(t *testing.T) {
	t.Parallel()

	svc, _ := cardioService(t, cardioScript(
		cardioJSON(cardioSleepPath(), `{"dailySleepDTO":null}`)))

	got, err := svc.readSleepSummary(cardioContext(t), cardioDate)
	if err != nil {
		t.Fatalf("readSleepSummary() = %v", err)
	}
	if got.HasData {
		t.Error("HasData = true for a night with no summary, want false")
	}
	if got.Date != cardioDate {
		t.Errorf("Date = %q, want the requested day", got.Date)
	}
}

func TestSleepSummaryLogValueReportsShapeOnly(t *testing.T) {
	t.Parallel()

	score := 81.0
	spo2 := 96.0
	got := SleepSummary{HasData: true, SleepScore: &score, AvgSpO2Percent: &spo2}

	rendered := got.LogValue().String()
	for _, forbidden := range []string{"81", "96"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("the log value %q carries a reading, want shape only", rendered)
		}
	}
	if !strings.Contains(rendered, "score=set") {
		t.Errorf("the log value %q does not report the score's presence", rendered)
	}
}

func TestReadSleepSummaryReportsAGarminFailureWithoutThePayload(t *testing.T) {
	t.Parallel()

	svc, _ := cardioService(t, cardioScript(cardioFailure(cardioSleepPath())))

	_, err := svc.readSleepSummary(cardioContext(t), cardioDate)
	assertSanitizedGarminFailure(t, err)
}

// cardioSleepSecondNight is the night before cardioDate, so the window read has two
// distinct nights to curate. Every figure is synthetic.
const cardioSleepSecondNight = `{"dailySleepDTO":{"calendarDate":"2026-01-30",` +
	`"sleepTimeSeconds":25200,"deepSleepSeconds":4800,"lightSleepSeconds":15000,` +
	`"remSleepSeconds":5400,"awakeSleepSeconds":540,` +
	`"sleepScores":{"overall":{"value":74,"qualifierKey":"FAIR"}}}}`

// The window every range test asks for: the two nights ending on cardioDate.
const (
	cardioRangeStart = "2026-01-30"
	cardioRangeEnd   = cardioDate
)

func TestReadSleepSummaryRangeReturnsEveryNightThatCarriedData(t *testing.T) {
	t.Parallel()

	// The path is the same for both nights — the day is a query parameter — and the
	// window fans out, so which queued body answers which night is not fixed. Both
	// bodies carry a summary, so both nights carry data either way.
	script := cardioScript().With(cardioSleepPath(),
		testkit.JSON(http.StatusOK, cardioSleepSecondNight),
		testkit.JSON(http.StatusOK, cardioSleepBody))
	svc, fake := cardioService(t, script)

	got, err := svc.readSleepSummaryRange(cardioContext(t), cardioRangeStart, cardioRangeEnd)
	if err != nil {
		t.Fatalf("readSleepSummaryRange() = %v", err)
	}

	if got.NightsRequested != 2 {
		t.Errorf("NightsRequested = %d, want 2", got.NightsRequested)
	}
	if got.NightsReturned != 2 || len(got.Nights) != 2 {
		t.Fatalf("the window returned %+v, want two nights", got.Nights)
	}
	if got.Nights[0].Date != cardioRangeStart {
		t.Errorf("Nights[0].Date = %q, want the older night first", got.Nights[0].Date)
	}
	if got.Nights[1].Date != cardioRangeEnd {
		t.Errorf("Nights[1].Date = %q, want %q", got.Nights[1].Date, cardioRangeEnd)
	}
	// The display name is resolved once for the whole window, not once a night.
	profileReads := 0
	for _, request := range fake.Requests() {
		if request.Path == client.PathSocialProfile {
			profileReads++
		}
	}
	if profileReads != 1 {
		t.Errorf("the window performed %d profile reads, want one for the whole window",
			profileReads)
	}
}

// TestReadSleepSummaryRangeOmitsANightWithNoData proves a night the device was not
// worn is left out and counted, rather than returned as an empty night. The window
// fans out, so which night receives the empty answer is not asserted — only that
// exactly one night is returned and that it names a day inside the window.
func TestReadSleepSummaryRangeOmitsANightWithNoData(t *testing.T) {
	t.Parallel()

	script := cardioScript().With(cardioSleepPath(),
		testkit.JSON(http.StatusOK, `{"dailySleepDTO":null}`),
		testkit.JSON(http.StatusOK, cardioSleepBody))
	svc, _ := cardioService(t, script)

	got, err := svc.readSleepSummaryRange(cardioContext(t), cardioRangeStart, cardioRangeEnd)
	if err != nil {
		t.Fatalf("readSleepSummaryRange() = %v", err)
	}

	if got.NightsRequested != 2 {
		t.Errorf("NightsRequested = %d, want 2", got.NightsRequested)
	}
	if got.NightsReturned != 1 || len(got.Nights) != 1 {
		t.Fatalf("the window returned %+v, want only the night that carried data", got.Nights)
	}
	if date := got.Nights[0].Date; date != cardioRangeStart && date != cardioRangeEnd {
		t.Errorf("Nights[0].Date = %q, want a night inside the window", date)
	}
}

// TestReadSleepSummaryRangeRefusesAnOversizedWindow keeps the bound at the boundary:
// nothing is dispatched for a window past the nightly-read bound.
func TestReadSleepSummaryRangeRefusesAnOversizedWindow(t *testing.T) {
	t.Parallel()

	svc, fake := cardioService(t, cardioScript(cardioJSON(cardioSleepPath(), cardioSleepBody)))

	_, err := svc.readSleepSummaryRange(cardioContext(t), "2025-01-01", cardioRangeEnd)
	if err == nil {
		t.Fatal("an oversized window was accepted")
	}
	if got := len(fake.Requests()); got != 0 {
		t.Errorf("dispatched %d requests for a refused window, want none", got)
	}
}

// TestReadSleepSummaryRangeReportsAGarminFailureWithoutThePayload proves a night that
// cannot be read fails the call with authored advice, never with Garmin's body.
func TestReadSleepSummaryRangeReportsAGarminFailureWithoutThePayload(t *testing.T) {
	t.Parallel()

	svc, _ := cardioService(t, cardioScript(cardioFailure(cardioSleepPath())))

	_, err := svc.readSleepSummaryRange(cardioContext(t), cardioRangeStart, cardioRangeEnd)
	assertSanitizedGarminFailure(t, err)
}

// TestSleepSummaryRangeLogValueReportsShapeOnly keeps the readings out of the logs.
func TestSleepSummaryRangeLogValueReportsShapeOnly(t *testing.T) {
	t.Parallel()

	score := 81.0
	got := SleepSummaryRange{
		NightsRequested: 2, NightsReturned: 1,
		Nights: []SleepSummary{{HasData: true, SleepScore: &score}},
	}

	rendered := got.LogValue().String()
	if strings.Contains(rendered, "81") {
		t.Errorf("the log value %q carries a reading, want shape only", rendered)
	}
}
