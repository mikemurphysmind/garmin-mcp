//go:build garminlive

package live

import (
	"testing"

	"github.com/tamcore/garmin-mcp/internal/tools"
)

// sampleCourseGPX is a minimal, synthetic GPX track: three points a few hundred
// metres apart, none of them a real place this account has ever visited. It exists
// only so Garmin's course-import parser has two or more points to build a course
// from (internal/garmin/api/coursesupload.go's buildCoursePayload requires at least
// two); the coordinates themselves are arbitrary and bounded, the same way every
// other figure this suite writes is.
const sampleCourseGPX = `<?xml version="1.0" encoding="UTF-8"?>
<gpx version="1.1" creator="garmin-mcp-live" xmlns="http://www.topografix.com/GPX/1/1">
  <trk>
    <trkseg>
      <trkpt lat="47.000000" lon="8.000000"><ele>400</ele></trkpt>
      <trkpt lat="47.001000" lon="8.001000"><ele>410</ele></trkpt>
      <trkpt lat="47.002000" lon="8.002000"><ele>420</ele></trkpt>
    </trkseg>
  </trk>
</gpx>`

// TestLiveCourseLifecycle drives one course from creation to removal:
// upload_course saves it, get_courses lists it back, and delete_course removes it.
//
// upload_course's ownership is proven differently than an activity's or a workout's:
// courseguard_test.go's adoptCourse binds the create response's identifier against a
// list search for the name this suite sent, because Garmin's course-service has no
// per-course GET the generic path (writeguard_test.go's adopt, writeverify_test.go's
// storedObject) could otherwise use. Whatever adoptCourse could not verify would stay
// unowned, and w.owned.owns below is the proof it did.
func TestLiveCourseLifecycle(t *testing.T) {
	w := liveWriteEnv(t)

	name := w.names.name(labelNameCourse)
	created := w.call(t, tools.ToolUploadCourse, map[string]any{
		argGPXContent: sampleCourseGPX, argCourseName: name,
	})
	id := identifier(t, created, tools.ToolUploadCourse, argCourseID)
	w.keepClean(t, kindCourse, id)

	if !w.owned.owns(kindCourse, id) {
		t.Fatal("the write guard did not learn the created course from Garmin's own response, " +
			"so delete_course would refuse to remove it even though this suite created it")
	}
	assertSuiteValue(t, tools.ToolUploadCourse, keyName, name, created)

	if !w.courseIsListed(t, id, name) {
		t.Errorf("%s reported success and %s does not list the created course",
			tools.ToolUploadCourse, tools.ToolGetCourses)
	}

	w.assertCourseDetailsMatchTheUpload(t, id, name)
	w.assertCourseRendersAsGPX(t, id)

	w.deleteCourseViaTool(t, id)
}

// assertCourseDetailsMatchTheUpload drives get_course_details against the course this
// suite created and compares it with what the upload sent.
//
// It runs here rather than in the read-only sweep because the tool needs a course
// identifier, and the read half must not read a course the account already had: a
// pre-existing course is somebody's real route.
func (w *writeEnv) assertCourseDetailsMatchTheUpload(t *testing.T, id int64, name string) {
	t.Helper()

	detail := w.call(t, tools.ToolGetCourseDetails, map[string]any{argCourseID: id})
	if got := identifier(t, detail, tools.ToolGetCourseDetails, argCourseID); got != id {
		t.Errorf("%s reported course %d, want the created %d",
			tools.ToolGetCourseDetails, got, id)
	}
	assertSuiteValue(t, tools.ToolGetCourseDetails, keyName, name, detail)

	// The uploaded GPX carries three track points, so the stored course must hold at
	// least that many: Garmin may add its own, and never fewer.
	points, ok := detail["geo_points_count"].(float64)
	if !ok {
		t.Fatalf("%s reported no geo_points_count", tools.ToolGetCourseDetails)
	}
	if int(points) < sampleCourseTrackPoints {
		t.Errorf("%s reported %d route points, want at least the %d the upload sent",
			tools.ToolGetCourseDetails, int(points), sampleCourseTrackPoints)
	}
}

// assertCourseRendersAsGPX drives download_course_gpx against the created course and
// checks the document it returns is a GPX carrying a track.
//
// The document is never written anywhere: it is asserted on in memory and dropped,
// the same way every other payload this suite reads is.
func (w *writeEnv) assertCourseRendersAsGPX(t *testing.T, id int64) {
	t.Helper()

	result := w.call(t, tools.ToolDownloadCourseGPX, map[string]any{argCourseID: id})
	if got := identifier(t, result, tools.ToolDownloadCourseGPX, "id"); got != id {
		t.Errorf("%s reported course %d, want the created %d",
			tools.ToolDownloadCourseGPX, got, id)
	}
	if got, _ := result["media_type"].(string); got != "application/gpx+xml" {
		t.Errorf("%s reported media type %q, want the GPX type",
			tools.ToolDownloadCourseGPX, got)
	}
	size, ok := result["bytes"].(float64)
	if !ok || size <= 0 {
		t.Errorf("%s reported %v bytes, want a rendered document",
			tools.ToolDownloadCourseGPX, result["bytes"])
	}
}

// sampleCourseTrackPoints is how many track points sampleCourseGPX carries.
const sampleCourseTrackPoints = 3

// argGPXContent and argCourseName are upload_course's own wire argument names
// (internal/tools/coursesupload.go's unexported argGPXContent and argCourseName),
// repeated here because this package cannot reach an unexported identifier of
// another one.
const (
	argGPXContent = "gpx_content"
	argCourseName = "course_name"
)

// courseIsListed reports whether get_courses lists one course by identifier and name.
func (w *writeEnv) courseIsListed(t *testing.T, id int64, name string) bool {
	t.Helper()

	library := w.call(t, tools.ToolGetCourses, nil)
	entries, _ := library["courses"].([]any)
	for _, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		listedID, ok := object[argCourseID].(float64)
		if !ok || int64(listedID) != id {
			continue
		}
		listedName, _ := object[keyName].(string)
		return listedName == name
	}
	return false
}

// deleteCourseViaTool drives delete_course over the write session and releases the
// course from the ledger once get_courses no longer lists it.
//
// It cannot reuse writecleanup_test.go's generic deleteViaTool: DeleteCourseResult
// (internal/tools/coursedelete.go) carries course_id, status and message, never the
// "deleted" boolean every other destructive tool's result shares, so the proof of
// success here is the echoed course_id plus the listing's own absence rather than
// that one shared field.
func (w *writeEnv) deleteCourseViaTool(t *testing.T, id int64) {
	t.Helper()

	asked := w.confirmations.Load()
	result := w.call(t, tools.ToolDeleteCourse, map[string]any{argCourseID: id})
	if got := identifier(t, result, tools.ToolDeleteCourse, argCourseID); got != id {
		t.Fatalf("%s reported a different course than the one deleted", tools.ToolDeleteCourse)
	}
	if w.confirmations.Load() == asked {
		t.Errorf("%s ran without asking for confirmation, so the destructive gate was not reached",
			tools.ToolDeleteCourse)
	}

	if !awaitAbsent(w.courseGone(t, id)) {
		t.Errorf("%s reported the course as deleted and %s still lists it; it stays in the "+
			"ledger so the cleanup removes it", tools.ToolDeleteCourse, tools.ToolGetCourses)
		return
	}
	w.owned.release(kindCourse, id)
}

// courseGone reports whether get_courses no longer lists one course.
//
// get_courses carries no authoritative not-found the way get_workout_by_id or
// get_activity does — a removed course is simply absent from the next listing rather
// than answered with a distinguishable refusal — so this is priced as the weaker,
// calendar-style absence proof rather than the record one. See absenceProof.
func (w *writeEnv) courseGone(t *testing.T, id int64) absenceProof {
	t.Helper()

	return calendarAbsence(func() bool {
		return !w.courseListed(t, id)
	})
}

// courseListed reports whether get_courses lists any course with this identifier,
// regardless of name.
func (w *writeEnv) courseListed(t *testing.T, id int64) bool {
	t.Helper()

	library := w.call(t, tools.ToolGetCourses, nil)
	entries, _ := library["courses"].([]any)
	for _, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if listedID, ok := object[argCourseID].(float64); ok && int64(listedID) == id {
			return true
		}
	}
	return false
}
