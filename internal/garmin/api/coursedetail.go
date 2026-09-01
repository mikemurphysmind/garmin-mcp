package api

import (
	"context"

	"github.com/tamcore/garmin-mcp/internal/garmin/client"
)

// The per-course read. It is the only course read that carries the route itself: the
// listing in courses.go carries totals, and this carries every recorded point.
//
// Source: courses.py:182-219 (get_course_details) and courses.py:224-292
// (download_course_gpx), both of which GET "/course-service/course/{course_id}" and
// read coursePoints and geoPoints out of the one document.
//
// Every point here is location data tied to a person. No model in this file is ever
// logged with its content, only with its shape.

// A CoursePoint is one custom waypoint on a course: a shop, water, food, a campground,
// a hazard.
//
// Source: courses.py:196-204, `cp.get("name")`, `cp.get("pointType")`, `cp.get("lat")`,
// `cp.get("lon")` and `cp.get("distance")`.
type CoursePoint struct {
	Name           client.Text   `json:"name"`
	PointType      client.Text   `json:"pointType"`
	Latitude       client.Number `json:"lat"`
	Longitude      client.Number `json:"lon"`
	DistanceMeters client.Number `json:"distance"`
}

// A CourseGeoPoint is one recorded track point of the course route.
//
// Source: courses.py:272-276, `p.get("latitude")`, `p.get("longitude")` and
// `p.get("elevation")`.
type CourseGeoPoint struct {
	Latitude  client.Number `json:"latitude"`
	Longitude client.Number `json:"longitude"`
	Elevation client.Number `json:"elevation"`
}

// CourseDetail is one course with its route.
//
// Garmin answers this document with two spellings for each of the three totals, and
// upstream reads both (`data.get("distanceInMeters") or data.get("distanceMeter")`), so
// both are decoded and the accessors report whichever the answer carried.
type CourseDetail struct {
	CourseID     client.Number       `json:"courseId"`
	Name         client.Text         `json:"courseName"`
	ActivityType *CourseActivityType `json:"activityType"`

	DistanceInMeters client.Number `json:"distanceInMeters"`
	DistanceMeter    client.Number `json:"distanceMeter"`

	ElevationGainInMeters client.Number `json:"elevationGainInMeters"`
	ElevationGainMeter    client.Number `json:"elevationGainMeter"`
	ElevationLossInMeters client.Number `json:"elevationLossInMeters"`
	ElevationLossMeter    client.Number `json:"elevationLossMeter"`

	CoursePoints []CoursePoint    `json:"coursePoints"`
	GeoPoints    []CourseGeoPoint `json:"geoPoints"`
}

// DistanceMeters reports the course distance from whichever field it arrived in.
func (c CourseDetail) DistanceMeters() (float64, bool) {
	return firstSetNumber(c.DistanceInMeters, c.DistanceMeter)
}

// ElevationGainMeters reports the ascent from whichever field it arrived in.
func (c CourseDetail) ElevationGainMeters() (float64, bool) {
	return firstSetNumber(c.ElevationGainInMeters, c.ElevationGainMeter)
}

// ElevationLossMeters reports the descent from whichever field it arrived in.
func (c CourseDetail) ElevationLossMeters() (float64, bool) {
	return firstSetNumber(c.ElevationLossInMeters, c.ElevationLossMeter)
}

// CourseDetails reads one course, with its waypoints and its recorded route.
//
// The identifier is a validated client.ID, so it is decimal digits only and can carry
// no path separator. The response is bounded by the request layer's own response and
// decompression bounds; a course route is long, and a caller bounds what it reports
// from it.
//
// The operation label is the caller's, because two tools read this one document and a
// log line should name which of them asked.
func (c *Courses) CourseDetails(
	ctx context.Context, session client.Session, id client.ID, op client.Op,
) (CourseDetail, error) {
	req := readRequest(op, client.EndpointCourseDetail,
		client.PathCourseBase+"/"+id.String(), nil)
	if err := requireID(req, id); err != nil {
		return CourseDetail{}, err
	}

	var detail CourseDetail
	if _, err := c.req.read(ctx, session, req, &detail); err != nil {
		return CourseDetail{}, err
	}
	return detail, nil
}
