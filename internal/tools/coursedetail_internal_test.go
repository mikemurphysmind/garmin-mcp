package tools

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tamcore/garmin-mcp/internal/garmin/client"
	"github.com/tamcore/garmin-mcp/internal/testkit"
)

// Every course fixture here is synthetic. The coordinates are invented, they name no
// real place, and no fixture is a recording of a real account.
const (
	detailCourseID   = 9101
	detailCoursePath = client.PathCourseBase + "/9101"

	// detailCourseName carries characters that must be escaped in the rendered
	// document, because a course name is account-supplied text.
	detailCourseName = `Loop & Trail <fast>`
)

// courseDetailBody is one course with two waypoints and three route points. The second
// waypoint carries no coordinate pair, which is a point the render must leave out, and
// the third route point carries no elevation.
const courseDetailBody = `{"courseId":9101,` +
	`"courseName":"Loop & Trail <fast>","activityType":{"typeKey":"running"},` +
	`"distanceInMeters":5230.5,"elevationGainInMeters":120,"elevationLossInMeters":118,` +
	`"coursePoints":[` +
	`{"name":"Water","pointType":"WATER","lat":52.5,"lon":13.4,"distance":1200},` +
	`{"name":"Unplaced","pointType":"FOOD"}],` +
	`"geoPoints":[{"latitude":52.5,"longitude":13.4,"elevation":34},` +
	`{"latitude":52.51,"longitude":13.41,"elevation":36},` +
	`{"latitude":52.52,"longitude":13.42}]}`

func getCourseDetailsRegistration() newToolsRegistration {
	return newToolsRegistration{
		name: ToolGetCourseDetails, register: registerGetCourseDetails,
	}
}

func downloadCourseGPXRegistration() newToolsRegistration {
	return newToolsRegistration{
		name: ToolDownloadCourseGPX, register: registerDownloadCourseGPX,
	}
}

// courseDetailSession stands up the two tools over one scripted course document.
func courseDetailSession(t *testing.T, body string) *mcp.ClientSession {
	t.Helper()

	svc, _ := newToolsService(t, testkit.NewScript().
		With(detailCoursePath, testkit.JSON(http.StatusOK, body)))
	server := newToolsServer(t, svc, newToolsServerConfig{
		readOnly: []newToolsRegistration{getCourseDetailsRegistration()},
		write:    []newToolsRegistration{downloadCourseGPXRegistration()},
	})
	return newToolsSession(t, server)
}

func TestGetCourseDetailsReturnsTheWaypointsAndTheRouteSize(t *testing.T) {
	t.Parallel()

	session := courseDetailSession(t, courseDetailBody)

	out := newToolsCall(t, session, ToolGetCourseDetails,
		map[string]any{argCourseID: detailCourseID})

	if got := out["course_id"]; got != float64(detailCourseID) {
		t.Errorf("course_id = %v, want %d", got, detailCourseID)
	}
	if got := out["name"]; got != detailCourseName {
		t.Errorf("name = %v, want %q", got, detailCourseName)
	}
	if got := out["activity"]; got != defaultCourseActivityType {
		t.Errorf("activity = %v, want %q", got, defaultCourseActivityType)
	}
	if got := number(t, out, "distance_m"); got != 5230.5 {
		t.Errorf("distance_m = %v, want 5230.5", got)
	}
	if got := number(t, out, "elevation_gain_m"); got != 120 {
		t.Errorf("elevation_gain_m = %v, want 120", got)
	}

	// The route itself is not returned; only how many points it holds.
	if got := number(t, out, "geo_points_count"); got != 3 {
		t.Errorf("geo_points_count = %v, want 3", got)
	}
	if _, present := out["geo_points"]; present {
		t.Error("the whole route reached the caller, want the count only")
	}

	waypoints := list(t, out, "waypoints")
	if len(waypoints) != 2 {
		t.Fatalf("waypoints holds %d entries, want both course points", len(waypoints))
	}
	first := entry(t, waypoints, 0)
	if got := first["type"]; got != "WATER" {
		t.Errorf("waypoints[0].type = %v, want WATER", got)
	}
	if got := number(t, first, "lat"); got != 52.5 {
		t.Errorf("waypoints[0].lat = %v, want 52.5", got)
	}
	if got := number(t, first, "distance_m"); got != 1200 {
		t.Errorf("waypoints[0].distance_m = %v, want 1200", got)
	}
	// A waypoint Garmin holds no coordinate for still reports its name and type.
	if _, present := entry(t, waypoints, 1)["lat"]; present {
		t.Error("a waypoint with no coordinate reported one")
	}
}

// TestGetCourseDetailsReadsTheAlternativeTotalSpellings proves both field spellings
// Garmin uses for the three totals decode.
func TestGetCourseDetailsReadsTheAlternativeTotalSpellings(t *testing.T) {
	t.Parallel()

	body := `{"courseId":9101,"courseName":"Alt","distanceMeter":1000,` +
		`"elevationGainMeter":50,"elevationLossMeter":40,` +
		`"geoPoints":[{"latitude":1,"longitude":2}]}`
	session := courseDetailSession(t, body)

	out := newToolsCall(t, session, ToolGetCourseDetails,
		map[string]any{argCourseID: detailCourseID})

	for field, want := range map[string]float64{
		"distance_m": 1000, "elevation_gain_m": 50, "elevation_loss_m": 40,
	} {
		if got := number(t, out, field); got != want {
			t.Errorf("%s = %v, want %v", field, got, want)
		}
	}
}

// TestGetCourseDetailsRefusesABadIdentifier keeps validation at the boundary.
func TestGetCourseDetailsRefusesABadIdentifier(t *testing.T) {
	t.Parallel()

	session := courseDetailSession(t, courseDetailBody)

	for name, id := range map[string]any{
		"unset":        0,
		caseNegative:   -1,
		"path segment": stressTraversal,
	} {
		t.Run(name, func(t *testing.T) {
			if advice := newToolsCallError(t, session, ToolGetCourseDetails,
				map[string]any{argCourseID: id}); advice == "" {
				t.Errorf("%s was accepted", name)
			}
		})
	}
}

func TestDownloadCourseGPXRendersAnEscapedDocument(t *testing.T) {
	t.Parallel()

	session := courseDetailSession(t, courseDetailBody)

	result := newToolsRawCall(t, session, ToolDownloadCourseGPX,
		map[string]any{argCourseID: detailCourseID})
	if result.IsError {
		t.Fatalf("download_course_gpx returned an error: %s", newToolsResultText(result))
	}

	document := string(embeddedBlob(t, result))
	for _, want := range []string{
		`<gpx version="1.1"`,
		`xmlns="http://www.topografix.com/GPX/1/1"`,
		`<wpt lat="52.5" lon="13.4">`,
		`<trkpt lat="52.52" lon="13.42">`,
		`<ele>34</ele>`,
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the document does not carry %q:\n%s", want, document)
		}
	}
	// The account-supplied name is escaped, never interpolated as markup.
	if strings.Contains(document, "<fast>") {
		t.Error("the course name reached the document as markup")
	}
	if !strings.Contains(document, "Loop &amp; Trail &lt;fast&gt;") {
		t.Errorf("the escaped course name is missing:\n%s", document)
	}
	// The waypoint with no coordinate pair is left out; the placed one is kept.
	if got := strings.Count(document, "<wpt "); got != 1 {
		t.Errorf("the document holds %d waypoints, want the one that has a coordinate", got)
	}
	// Every route point is rendered, and the one with no elevation carries none.
	if got := strings.Count(document, "<trkpt "); got != 3 {
		t.Errorf("the document holds %d track points, want three", got)
	}
	if got := strings.Count(document, "<ele>"); got != 2 {
		t.Errorf("the document holds %d elevations, want the two Garmin holds", got)
	}

	structured := newToolsCall(t, session, ToolDownloadCourseGPX,
		map[string]any{argCourseID: detailCourseID})
	if got := structured["uri"]; got != courseResourceStart+"9101.gpx" {
		t.Errorf("uri = %v, want the course resource URI", got)
	}
	if got := structured["media_type"]; got != courseGPXMediaType {
		t.Errorf("media_type = %v, want %q", got, courseGPXMediaType)
	}
	if got := number(t, structured, "bytes"); got <= 0 {
		t.Errorf("bytes = %v, want the rendered size", got)
	}
}

// TestDownloadCourseGPXRefusesACourseWithNoRoute proves a course that holds no
// recorded route is refused rather than answered with an empty document.
func TestDownloadCourseGPXRefusesACourseWithNoRoute(t *testing.T) {
	t.Parallel()

	session := courseDetailSession(t,
		`{"courseId":9101,"courseName":"Empty","geoPoints":[]}`)

	if advice := newToolsCallError(t, session, ToolDownloadCourseGPX,
		map[string]any{argCourseID: detailCourseID}); advice == "" {
		t.Error("a course with no route was rendered")
	}
}

// TestDownloadCourseGPXDeclaresNoFilesystemPath keeps the download policy honest: the
// contract declares no output directory at all, where upstream writes a file.
func TestDownloadCourseGPXDeclaresNoFilesystemPath(t *testing.T) {
	t.Parallel()

	for _, property := range downloadCourseGPXContract().Schema.Properties() {
		switch property.Name {
		case "output_dir", "output_path", "target":
			t.Errorf("the contract declares %q, which would name a server path",
				property.Name)
		}
	}
}

// TestCourseDetailLogValueReportsShapeOnly keeps a coordinate and a course name out of
// the logs.
func TestCourseDetailLogValueReportsShapeOnly(t *testing.T) {
	t.Parallel()

	name := detailCourseName
	latitude := 52.5
	out := CourseDetail{
		CourseID: detailCourseID, Name: &name, GeoPointsCount: 3,
		Waypoints: []CourseWaypoint{{Name: &name, Latitude: &latitude}},
	}

	rendered := out.LogValue().String()
	for _, forbidden := range []string{"52.5", "Loop"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("the log value %q carries %q", rendered, forbidden)
		}
	}
}

// embeddedBlob returns the bytes of the result's one embedded resource.
func embeddedBlob(t *testing.T, result *mcp.CallToolResult) []byte {
	t.Helper()

	for _, content := range result.Content {
		embedded, ok := content.(*mcp.EmbeddedResource)
		if !ok || embedded.Resource == nil {
			continue
		}
		if embedded.Resource.Blob != nil {
			return embedded.Resource.Blob
		}
	}

	// The SDK may have encoded the blob into its wire form already; decode that
	// rather than failing on a shape that carries the same bytes.
	encoded, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshalling the result content: %v", err)
	}
	var wire []struct {
		Resource struct {
			Blob string `json:"blob"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decoding the result content: %v", err)
	}
	for _, item := range wire {
		if item.Resource.Blob == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(item.Resource.Blob)
		if err != nil {
			t.Fatalf("decoding the embedded blob: %v", err)
		}
		return decoded
	}
	t.Fatal("the result carries no embedded resource")
	return nil
}
