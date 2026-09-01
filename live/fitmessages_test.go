//go:build garminlive

package live

import (
	"testing"

	"github.com/tamcore/garmin-mcp/internal/tools"
)

// This file drives get_activity_fit_messages against the same activity the FIT
// cross-checks analyse.
//
// It is separate from the read-only sweep because the tool needs an activity
// identifier a prior read produced, and because the sweep would otherwise download a
// device file for a tool the agreement test already downloads one for.
//
// Nothing here prints a reading: the assertions are on counts, on the pagination and
// on which field names came back suppressed.

// The argument names and values this file sends, named once.
const (
	argIncludeRecords = "include_records"
	argMessageTypes   = "message_types"
	argMessageLimit   = "message_limit"

	fitRecordType  = "record"
	fitSessionType = "session"

	// fitMessagePage is how many messages one live page asks for. It is small on
	// purpose: the point is the paging contract, not the volume.
	fitMessagePage = 25
)

// TestActivityFITMessagesInspectTheDeviceFile proves the inspection answers for a real
// device file: the inventory names the types the file holds, the default page carries
// no record, and every coordinate the device wrote is withheld.
func TestActivityFITMessagesInspectTheDeviceFile(t *testing.T) {
	e := liveEnv(t)
	a := analysedActivity(t)

	result := e.call(t, tools.ToolGetActivityFITMessages, map[string]any{
		argActivityID: a.id.String(),
	})

	counts, ok := result["message_counts"].(map[string]any)
	if !ok || len(counts) == 0 {
		t.Fatalf("%s reported no message counts", tools.ToolGetActivityFITMessages)
	}
	if _, named := counts[fitSessionType]; !named {
		t.Errorf("%s reported no session in a file the api layer decoded one from",
			tools.ToolGetActivityFITMessages)
	}
	if size, ok := result["fit_size_bytes"].(float64); !ok || int(size) != a.fileSize {
		t.Errorf("%s reported a different downloaded size than the direct download",
			tools.ToolGetActivityFITMessages)
	}

	assertNoRecordInThePage(t, result)
	assertPaginationIsCoherent(t, result)

	// With the record stream requested, the coordinates the device wrote must come
	// back named and withheld rather than as values.
	withRecords := e.call(t, tools.ToolGetActivityFITMessages, map[string]any{
		argActivityID:     a.id.String(),
		argIncludeRecords: true,
		argMessageTypes:   []any{fitRecordType},
		argMessageLimit:   fitMessagePage,
	})
	assertCoordinatesAreWithheld(t, withRecords)
}

// assertNoRecordInThePage requires the default page to leave the per-second stream out.
func assertNoRecordInThePage(t *testing.T, result map[string]any) {
	t.Helper()

	messages, _ := result["messages"].([]any)
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("%s returned a message that is not an object",
				tools.ToolGetActivityFITMessages)
		}
		if name, _ := message["type"].(string); name == fitRecordType {
			t.Fatalf("%s returned a record on the default page, which excludes them",
				tools.ToolGetActivityFITMessages)
		}
	}
}

// assertPaginationIsCoherent requires the envelope to describe the page it carries.
func assertPaginationIsCoherent(t *testing.T, result map[string]any) {
	t.Helper()

	pagination, ok := result["pagination"].(map[string]any)
	if !ok {
		t.Fatalf("%s returned no pagination envelope", tools.ToolGetActivityFITMessages)
	}
	messages, _ := result["messages"].([]any)
	returned, ok := pagination["returned_count"].(float64)
	if !ok || int(returned) != len(messages) {
		t.Errorf("%s reported returned_count %v for %d messages",
			tools.ToolGetActivityFITMessages, pagination["returned_count"], len(messages))
	}
	total, ok := pagination["total_selected"].(float64)
	if !ok || int(total) < len(messages) {
		t.Errorf("%s reported total_selected %v below the %d messages it returned",
			tools.ToolGetActivityFITMessages, pagination["total_selected"], len(messages))
	}
}

// assertCoordinatesAreWithheld requires every position field to be named and empty.
//
// A file recorded indoors carries none, which is why the absence of a position field
// is reported rather than failed: the assertion is that no coordinate has a value.
func assertCoordinatesAreWithheld(t *testing.T, result map[string]any) {
	t.Helper()

	messages, _ := result["messages"].([]any)
	positions := 0
	for _, item := range messages {
		message, _ := item.(map[string]any)
		fields, _ := message["fields"].([]any)
		for _, entry := range fields {
			field, _ := entry.(map[string]any)
			name, _ := field["name"].(string)
			if name != "position_lat" && name != "position_long" {
				continue
			}
			positions++
			if suppressed, _ := field["suppressed"].(bool); !suppressed {
				t.Errorf("%s returned %s without marking it suppressed",
					tools.ToolGetActivityFITMessages, name)
			}
			if _, carried := field["value"]; carried {
				t.Errorf("%s returned a value for %s",
					tools.ToolGetActivityFITMessages, name)
			}
		}
	}
	if positions == 0 {
		t.Log("the analysed activity carries no position field, so the suppression was " +
			"not exercised here; the unit tests cover it against a file that does")
	}
}
