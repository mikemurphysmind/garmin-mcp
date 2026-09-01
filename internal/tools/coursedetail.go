package tools

import (
	"context"
	"encoding/xml"
	"log/slog"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tamcore/garmin-mcp/internal/garmin/api"
	"github.com/tamcore/garmin-mcp/internal/garmin/client"
	"github.com/tamcore/garmin-mcp/internal/mcpserver"
	"github.com/tamcore/garmin-mcp/internal/policy"
)

// The two per-course reads. Both are additions beyond the pinned manifest: upstream
// added them after the pinned commit. They read one document and share its curation,
// so they share a file.
const (
	ToolGetCourseDetails  = "get_course_details"
	ToolDownloadCourseGPX = "download_course_gpx"
)

// DefaultMaxCourseWaypoints bounds the waypoint list one course detail returns. A
// course carries as many custom waypoints as its author added, so the list needs a
// ceiling of its own.
const DefaultMaxCourseWaypoints = 500

// MaxCourseTrackPoints bounds how many recorded track points one GPX may carry. A
// rendered point is roughly 80 bytes, so this keeps the document well inside the
// download bound before the bound itself is applied.
const MaxCourseTrackPoints = 40_000

// courseResourceStart is the URI prefix of a rendered course file.
const courseResourceStart = "garmin://course/"

// courseGPXMediaType is the media type this server labels the rendered document with.
const courseGPXMediaType = "application/gpx+xml"

// courseGPXFormat is the format label the result reports.
const courseGPXFormat = "gpx"

// A CourseWaypoint is one custom waypoint of a course.
//
// It carries a coordinate, which is location data tied to a person: it is never
// logged, and the log value of every model here reports shape alone.
type CourseWaypoint struct {
	Name           *string  `json:"name,omitempty" jsonschema:"the waypoint's name"`
	Type           *string  `json:"type,omitempty" jsonschema:"Garmin's point type"`
	Latitude       *float64 `json:"lat,omitempty" jsonschema:"the latitude, degrees"`
	Longitude      *float64 `json:"lon,omitempty" jsonschema:"the longitude, degrees"`
	DistanceMeters *float64 `json:"distance_m,omitempty" jsonschema:"metres into the course"`
}

// CourseDetail is one course with its waypoints.
//
// The recorded route is not returned here: a course holds thousands of track points,
// which is a file rather than a result. geo_points_count says how many it holds, and
// download_course_gpx returns them as a document.
type CourseDetail struct {
	CourseID int64   `json:"course_id" jsonschema:"the course identifier"`
	Name     *string `json:"name,omitempty" jsonschema:"the course name"`
	Activity *string `json:"activity,omitempty" jsonschema:"the activity type key"`

	DistanceMeters      *float64 `json:"distance_m,omitempty" jsonschema:"the course distance"`
	ElevationGainMeters *float64 `json:"elevation_gain_m,omitempty" jsonschema:"the total ascent"`
	ElevationLossMeters *float64 `json:"elevation_loss_m,omitempty" jsonschema:"the total descent"`

	WaypointsCount int              `json:"waypoints_count" jsonschema:"how many waypoints"`
	Waypoints      []CourseWaypoint `json:"waypoints" jsonschema:"the custom waypoints"`
	GeoPointsCount int              `json:"geo_points_count" jsonschema:"how many track points"`
	Truncated      bool             `json:"truncated" jsonschema:"whether the list was cut"`
}

// LogValue reports the counts, never a coordinate or a name.
func (d CourseDetail) LogValue() slog.Value {
	return shape("courseDetail",
		slog.Int("waypoints", len(d.Waypoints)),
		slog.Int("geoPoints", d.GeoPointsCount),
		slog.Bool("truncated", d.Truncated),
	)
}

// courseDetailInput is the strict argument set of both tools: one course identifier.
type courseDetailInput struct {
	CourseID any `json:"course_id" jsonschema:"the course identifier, from get_courses"`
}

// courseIDProperty declares the course identifier both tools take.
func courseIDProperty(purpose string) Property {
	return Property{
		Name: argCourseID, Types: []string{typeInteger},
		Description: purpose + ", from get_courses",
		Minimum:     bound(1),
		Required:    true,
	}
}

func getCourseDetailsContract() Contract {
	return Contract{
		Spec: mcpserver.ToolSpec{
			Name:  ToolGetCourseDetails,
			Title: "Get a course's details",
			Description: "read one course: its name, activity type, distance, total ascent " +
				"and descent, and every custom waypoint its author added, each with its " +
				"coordinate and how far into the course it sits. The recorded route is not " +
				"returned — a course holds thousands of track points — but " +
				"geo_points_count reports how many it holds, and download_course_gpx " +
				"returns them as a GPX document",
			Tier:        policy.TierReadOnly,
			Category:    categoryLocation,
			Annotations: readOnlyAnnotations(),
		},
		Schema: NewSchema(courseIDProperty("the course to read")),
	}
}

func downloadCourseGPXContract() Contract {
	return Contract{
		Spec: mcpserver.ToolSpec{
			Name:  ToolDownloadCourseGPX,
			Title: "Download a course as GPX",
			Description: "render one course as a GPX 1.1 document and return it as an " +
				"embedded MCP resource: one waypoint per custom course point and one track " +
				"point per recorded route point, with elevation where Garmin holds it. " +
				"Nothing is written to this server's filesystem, no directory is accepted " +
				"or remembered, and a course whose route is larger than this server's " +
				"bound is refused rather than truncated",
			Tier:        policy.TierWrite,
			Category:    categoryLocation,
			Annotations: writeAnnotations(true),
		},
		Schema: NewSchema(courseIDProperty("the course to render")),
	}
}

// registerGetCourseDetails registers the detail read.
func registerGetCourseDetails(registry *mcpserver.Registry, svc *service) error {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in courseDetailInput) (
		*mcp.CallToolResult, CourseDetail, error,
	) {
		detail, _, err := svc.readCourseDetail(ctx, in, client.OpGetCourseDetails)
		if err != nil {
			return nil, CourseDetail{}, err
		}
		return nil, detail, nil
	}
	return mcpserver.AddTool(registry, getCourseDetailsContract().Registration(), handler)
}

// registerDownloadCourseGPX registers the GPX render.
//
// It is write-tier for the same reason download_activity_file is: it moves a whole
// route out of the account in one call. It mutates nothing.
func registerDownloadCourseGPX(registry *mcpserver.Registry, svc *service) error {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in courseDetailInput) (
		*mcp.CallToolResult, DownloadedFile, error,
	) {
		detail, course, err := svc.readCourseDetail(ctx, in, client.OpDownloadCourseGPX)
		if err != nil {
			return nil, DownloadedFile{}, err
		}
		document, err := courseGPXDocument(course, svc.bounds.MaxDownloadBytes)
		if err != nil {
			return nil, DownloadedFile{}, err
		}

		uri := courseResourceStart + strconv.FormatInt(detail.CourseID, 10) + "." +
			courseGPXFormat
		out := DownloadedFile{
			ID:        detail.CourseID,
			Format:    courseGPXFormat,
			MediaType: courseGPXMediaType,
			Bytes:     len(document),
			URI:       uri,
		}
		return blobResult(uri, courseGPXMediaType, document), out, nil
	}
	return mcpserver.AddTool(registry, downloadCourseGPXContract().Registration(), handler)
}

// courseGPXDocument renders the course and applies both bounds: the point count this
// server will render, and the byte size it will inline into a result.
func courseGPXDocument(course api.CourseDetail, maxBytes int64) ([]byte, error) {
	switch {
	case len(course.GeoPoints) == 0:
		return nil, invalidArgument(
			"this course holds no recorded route, so there is nothing to render")
	case len(course.GeoPoints) > MaxCourseTrackPoints:
		return nil, tooLarge(
			"the course route holds more track points than this server will render")
	}

	document, err := renderCourseGPX(course)
	if err != nil {
		return nil, err
	}
	if int64(len(document)) > maxBytes {
		return nil, tooLarge(
			"the rendered course is larger than this server will inline into a result")
	}
	return document, nil
}

// readCourseDetail reads one course and curates it, returning both the curated result
// and the document behind it, because the GPX render needs the route the curation
// deliberately leaves out.
func (s *service) readCourseDetail(
	ctx context.Context, in courseDetailInput, op client.Op,
) (CourseDetail, api.CourseDetail, error) {
	id, err := parseIdentifier(argCourseID, in.CourseID)
	if err != nil {
		return CourseDetail{}, api.CourseDetail{}, err
	}
	session, err := s.session(ctx)
	if err != nil {
		return CourseDetail{}, api.CourseDetail{}, err
	}

	course, err := s.courses.CourseDetails(ctx, session, id, op)
	if err != nil {
		return CourseDetail{}, api.CourseDetail{}, fail(err)
	}
	return newCourseDetail(id, course), course, nil
}

// newCourseDetail curates one course, bounding the waypoint list.
func newCourseDetail(id client.ID, course api.CourseDetail) CourseDetail {
	out := CourseDetail{
		CourseID:       id.Int64(),
		Name:           optionalText(course.Name),
		GeoPointsCount: len(course.GeoPoints),
		Waypoints:      []CourseWaypoint{},
	}
	if activity := course.ActivityType; activity != nil {
		out.Activity = optionalText(activity.TypeKey)
	}
	if distance, ok := course.DistanceMeters(); ok {
		out.DistanceMeters = &distance
	}
	if gain, ok := course.ElevationGainMeters(); ok {
		out.ElevationGainMeters = &gain
	}
	if loss, ok := course.ElevationLossMeters(); ok {
		out.ElevationLossMeters = &loss
	}

	points := course.CoursePoints
	if len(points) > DefaultMaxCourseWaypoints {
		points = points[:DefaultMaxCourseWaypoints]
		out.Truncated = true
	}
	for _, point := range points {
		out.Waypoints = append(out.Waypoints, CourseWaypoint{
			Name:           optionalText(point.Name),
			Type:           optionalText(point.PointType),
			Latitude:       optionalFloat(point.Latitude),
			Longitude:      optionalFloat(point.Longitude),
			DistanceMeters: optionalFloat(point.DistanceMeters),
		})
	}
	out.WaypointsCount = len(out.Waypoints)
	return out
}

// The GPX document this server renders. The structs exist so encoding/xml escapes
// every value: a course name, a waypoint name and a point type are all
// account-supplied text, and upstream interpolates them into the document unescaped.
type gpxDocument struct {
	XMLName   xml.Name      `xml:"gpx"`
	Version   string        `xml:"version,attr"`
	Creator   string        `xml:"creator,attr"`
	Namespace string        `xml:"xmlns,attr"`
	Metadata  gpxMetadata   `xml:"metadata"`
	Waypoints []gpxWaypoint `xml:"wpt"`
	Track     gpxTrack      `xml:"trk"`
}

type gpxMetadata struct {
	Name string `xml:"name"`
}

type gpxWaypoint struct {
	Latitude  float64 `xml:"lat,attr"`
	Longitude float64 `xml:"lon,attr"`
	Name      string  `xml:"name"`
	Type      string  `xml:"type"`
}

type gpxTrack struct {
	Name    string     `xml:"name"`
	Segment gpxSegment `xml:"trkseg"`
}

type gpxSegment struct {
	Points []gpxTrackPoint `xml:"trkpt"`
}

type gpxTrackPoint struct {
	Latitude  float64  `xml:"lat,attr"`
	Longitude float64  `xml:"lon,attr"`
	Elevation *float64 `xml:"ele,omitempty"`
}

// The fixed GPX attributes and fallback labels this server writes.
const (
	gpxVersion   = "1.1"
	gpxCreator   = "garmin-mcp"
	gpxNamespace = "http://www.topografix.com/GPX/1/1"

	// These stand in for text Garmin holds none of, so a document always names what
	// it carries. Source: upstream's own fallbacks, `cp.get("name") or
	// cp.get("pointType") or "Waypoint"` and `data.get("courseName", ...)`.
	gpxDefaultWaypointName = "Waypoint"
	gpxDefaultCourseName   = "Garmin course"
)

// renderCourseGPX renders one course as a GPX 1.1 document.
//
// A waypoint or a track point Garmin holds no coordinate pair for is left out: a
// half-known point is not a point. Every text value goes through encoding/xml, so an
// account-supplied name cannot inject markup into the document.
func renderCourseGPX(course api.CourseDetail) ([]byte, error) {
	name := firstText(course.Name, gpxDefaultCourseName)
	document := gpxDocument{
		Version:   gpxVersion,
		Creator:   gpxCreator,
		Namespace: gpxNamespace,
		Metadata:  gpxMetadata{Name: name},
		Track:     gpxTrack{Name: name},
	}

	for _, point := range course.CoursePoints {
		latitude, hasLatitude := point.Latitude.Float64()
		longitude, hasLongitude := point.Longitude.Float64()
		if !hasLatitude || !hasLongitude {
			continue
		}
		document.Waypoints = append(document.Waypoints, gpxWaypoint{
			Latitude:  latitude,
			Longitude: longitude,
			Name:      courseWaypointName(point),
			Type:      firstText(point.PointType, gpxDefaultWaypointName),
		})
	}
	for _, point := range course.GeoPoints {
		latitude, hasLatitude := point.Latitude.Float64()
		longitude, hasLongitude := point.Longitude.Float64()
		if !hasLatitude || !hasLongitude {
			continue
		}
		document.Track.Segment.Points = append(document.Track.Segment.Points,
			gpxTrackPoint{
				Latitude:  latitude,
				Longitude: longitude,
				Elevation: optionalFloat(point.Elevation),
			})
	}

	encoded, err := xml.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, tooLarge("the course could not be rendered as a GPX document")
	}
	return append([]byte(xml.Header), encoded...), nil
}

// courseWaypointName names one waypoint, falling back to its type and then to a fixed
// label, exactly as upstream's own fallback chain does.
func courseWaypointName(point api.CoursePoint) string {
	return firstText(point.Name, firstText(point.PointType, gpxDefaultWaypointName))
}

// firstText reports the text value, or fallback when Garmin holds none or holds only
// space.
func firstText(text client.Text, fallback string) string {
	if value, ok := text.Value(); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
