package tools

import (
	"testing"

	"github.com/tamcore/garmin-mcp/internal/testkit"
)

// The two message types these tests name.
const (
	fitTypeRecord  = "record"
	fitTypeSession = "session"
)

// fitMessagesRide builds a synthetic file whose records carry a position, so the
// suppression can be proven on a file that really holds coordinates. Nothing here is
// a recorded location: both degrees are invented.
func fitMessagesRide(seconds int) []byte {
	samples := make([]testkit.FITSample, 0, seconds)
	for second := range seconds {
		samples = append(samples, testkit.FITSample{
			Second:    second,
			Latitude:  new(47.0 + float64(second)/10000),
			Longitude: new(8.0 + float64(second)/10000),
			Power:     new(fitTestPower),
			Cadence:   new(fitTestCadence),
			HeartRate: new(fitTestHeartRate),
		})
	}
	file := testkit.FITFile{
		Sport: 2, Session: true, Samples: samples,
		Laps: []testkit.FITLapFixture{{StartSecond: 0, EndSecond: seconds - 1}},
	}
	return testkit.ZipFIT("activity.fit", file.Bytes())
}

// fitMessagesInputFor is the argument set every test here starts from.
func fitMessagesInputFor() fitMessagesInput {
	return fitMessagesInput{ActivityID: int64(fitTestActivity)}
}

// TestFITMessagesInventoriesTheWholeFileAndPagesTheSelection proves the two halves of
// the result: the counts cover the file, and the messages are the selected page.
func TestFITMessagesInventoriesTheWholeFileAndPagesTheSelection(t *testing.T) {
	t.Parallel()

	h := newFITHarness(t, fitScript(fitMessagesRide(30)), Bounds{})

	out, err := h.svc.activityFITMessages(h.ctx, fitMessagesInputFor())
	if err != nil {
		t.Fatalf("activityFITMessages() = %v", err)
	}

	if out.ActivityID != fitTestActivity {
		t.Errorf("ActivityID = %d, want %d", out.ActivityID, fitTestActivity)
	}
	if out.Source != fitMessagesSource {
		t.Errorf("Source = %q, want %q", out.Source, fitMessagesSource)
	}
	if out.FileBytes <= 0 {
		t.Errorf("FileBytes = %d, want the downloaded size", out.FileBytes)
	}

	// The inventory covers the whole file, records included.
	if got := out.MessageCounts[fitTypeRecord]; got != 30 {
		t.Errorf("message_counts[record] = %d, want 30", got)
	}
	if out.RecordCount != 30 {
		t.Errorf("RecordCount = %d, want 30", out.RecordCount)
	}
	for _, wanted := range []string{fitTypeSession, "lap"} {
		if out.MessageCounts[wanted] == 0 {
			t.Errorf("message_counts holds no %s, want the file's own", wanted)
		}
	}

	// The default selection leaves the record stream out.
	if out.RecordsIncluded {
		t.Error("RecordsIncluded = true by default, want false")
	}
	for _, message := range out.Messages {
		if message.Type == fitTypeRecord {
			t.Fatal("a record reached the default page, want records excluded")
		}
	}
	if out.Pagination.ReturnedCount != len(out.Messages) {
		t.Errorf("returned_count = %d, want %d", out.Pagination.ReturnedCount,
			len(out.Messages))
	}
	if out.Pagination.TotalSelected != len(out.Messages) {
		t.Errorf("total_selected = %d, want the whole non-record stream %d",
			out.Pagination.TotalSelected, len(out.Messages))
	}
	if out.Pagination.NextOffset != nil {
		t.Errorf("next_offset = %v, want none when the page holds everything",
			*out.Pagination.NextOffset)
	}
}

// TestFITMessagesSuppressesEveryCoordinate is the privacy check: a file whose records
// carry a position must produce a result that names the field and carries no value.
func TestFITMessagesSuppressesEveryCoordinate(t *testing.T) {
	t.Parallel()

	h := newFITHarness(t, fitScript(fitMessagesRide(5)), Bounds{})

	in := fitMessagesInputFor()
	in.IncludeRecords = new(true)
	out, err := h.svc.activityFITMessages(h.ctx, in)
	if err != nil {
		t.Fatalf("activityFITMessages() = %v", err)
	}

	if !out.RecordsIncluded {
		t.Fatal("RecordsIncluded = false, want the requested record stream")
	}
	positions := 0
	for _, message := range out.Messages {
		for _, field := range message.Fields {
			switch field.Name {
			case "position_lat", "position_long":
				positions++
				if !field.Suppressed {
					t.Errorf("%s is not marked suppressed", field.Name)
				}
				if field.Value != nil {
					t.Errorf("%s carries a value, want it withheld", field.Name)
				}
			default:
				if field.Suppressed {
					t.Errorf("%s is marked suppressed, want only coordinates withheld",
						field.Name)
				}
			}
		}
	}
	if positions == 0 {
		t.Fatal("no position field was reported, so the suppression proved nothing: " +
			"the fixture carries coordinates on every record")
	}
}

// TestFITMessagesReturnsTheRequestedTypesOnly proves the type filter narrows the
// stream while the inventory still covers the file.
func TestFITMessagesReturnsTheRequestedTypesOnly(t *testing.T) {
	t.Parallel()

	h := newFITHarness(t, fitScript(fitMessagesRide(20)), Bounds{})

	in := fitMessagesInputFor()
	in.MessageTypes = []string{"SESSION"}
	out, err := h.svc.activityFITMessages(h.ctx, in)
	if err != nil {
		t.Fatalf("activityFITMessages() = %v", err)
	}

	if len(out.Messages) == 0 {
		t.Fatal("the session filter returned nothing, want the file's session")
	}
	for _, message := range out.Messages {
		if message.Type != fitTypeSession {
			t.Errorf("the page carries a %s, want sessions only", message.Type)
		}
	}
	// The filter is case-insensitive, and the inventory is unfiltered.
	if out.MessageCounts[fitTypeRecord] != 20 {
		t.Errorf("message_counts[record] = %d, want the unfiltered 20",
			out.MessageCounts[fitTypeRecord])
	}
}

// TestFITMessagesPagesTheRecordStream proves the page window and the next offset.
func TestFITMessagesPagesTheRecordStream(t *testing.T) {
	t.Parallel()

	h := newFITHarness(t, fitScript(fitMessagesRide(30)), Bounds{})

	in := fitMessagesInputFor()
	in.MessageTypes = []string{fitTypeRecord}
	in.IncludeRecords = new(true)
	in.MessageLimit = new(10)
	first, err := h.svc.activityFITMessages(h.ctx, in)
	if err != nil {
		t.Fatalf("activityFITMessages() = %v", err)
	}

	if len(first.Messages) != 10 {
		t.Fatalf("the first page holds %d messages, want the requested 10",
			len(first.Messages))
	}
	if first.Pagination.TotalSelected != 30 {
		t.Errorf("total_selected = %d, want the 30 records", first.Pagination.TotalSelected)
	}
	if first.Pagination.NextOffset == nil || *first.Pagination.NextOffset != 10 {
		t.Fatalf("next_offset = %v, want 10", first.Pagination.NextOffset)
	}

	in.MessageOffset = new(*first.Pagination.NextOffset)
	second, err := h.svc.activityFITMessages(h.ctx, in)
	if err != nil {
		t.Fatalf("activityFITMessages() = %v", err)
	}
	if len(second.Messages) != 10 {
		t.Fatalf("the second page holds %d messages, want 10", len(second.Messages))
	}
	// The pages do not overlap: the second starts where the first ended.
	if second.Messages[0].TypeIndex != 10 {
		t.Errorf("the second page starts at type index %d, want 10",
			second.Messages[0].TypeIndex)
	}

	in.MessageOffset = new(25)
	last, err := h.svc.activityFITMessages(h.ctx, in)
	if err != nil {
		t.Fatalf("activityFITMessages() = %v", err)
	}
	if len(last.Messages) != 5 {
		t.Errorf("the last page holds %d messages, want the remaining 5", len(last.Messages))
	}
	if last.Pagination.NextOffset != nil {
		t.Errorf("next_offset = %v, want none on the last page", *last.Pagination.NextOffset)
	}
}

// TestFITMessagesRefusesAnUnacceptablePage keeps the page bounds at the boundary, and
// proves nothing is downloaded for a refused argument.
func TestFITMessagesRefusesAnUnacceptablePage(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*fitMessagesInput){
		"negative offset": func(in *fitMessagesInput) { in.MessageOffset = new(-1) },
		"unset limit":     func(in *fitMessagesInput) { in.MessageLimit = new(0) },
		"oversized limit": func(in *fitMessagesInput) { in.MessageLimit = new(5001) },
		"too many types": func(in *fitMessagesInput) {
			types := make([]string, maxFITMessageTypes+1)
			for index := range types {
				types[index] = fitTypeSession
			}
			in.MessageTypes = types
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newFITHarness(t, fitScript(fitMessagesRide(5)), Bounds{})

			in := fitMessagesInputFor()
			mutate(&in)
			if _, err := h.svc.activityFITMessages(h.ctx, in); err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if got := len(h.fake.Requests()); got != 0 {
				t.Errorf("dispatched %d requests for a refused argument, want none", got)
			}
		})
	}
}

// TestFITMessagesLogValueReportsShapeOnly keeps a field value out of the logs.
func TestFITMessagesLogValueReportsShapeOnly(t *testing.T) {
	t.Parallel()

	out := FITMessages{
		MessageCounts: map[string]int{fitTypeRecord: 30},
		Pagination:    FITMessagePage{ReturnedCount: 1},
		Messages: []FITMessage{{Type: fitTypeRecord, Fields: []FITMessageField{
			{Name: "heart_rate", Value: fitTestHeartRate},
		}}},
	}

	rendered := out.LogValue().String()
	if contains(rendered, "145") {
		t.Errorf("the log value %q carries a reading", rendered)
	}
	if !contains(rendered, "fitMessages") {
		t.Errorf("the log value %q does not name the model", rendered)
	}
}
