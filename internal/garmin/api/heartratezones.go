package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/tamcore/garmin-mcp/internal/garmin/client"
)

// The account-level heart-rate zone configuration. Garmin stores one profile per
// sport: a generic DEFAULT profile plus optional overrides such as RUNNING and
// CYCLING, all served from and written to one document.
//
// Source: _HEART_RATE_ZONES_URL ("/biometric-service/heartRateZones") in upstream's
// user_profile.py, read by _get_heart_rate_zone_configs and written by
// set_heart_rate_zones as a PUT of an array of changed profiles. Garmin answers that
// PUT with 204 No Content, which the shared write path already treats as success.

// HeartRateZoneSport is a validated Garmin sport key for a zone profile.
//
// Only a parsed value reaches a request body, so a caller-supplied sport can carry no
// separator, no space and no lower-case drift. Source: _normalize_hr_zone_sport, which
// upper-cases, replaces spaces and hyphens with underscores, maps GENERIC to DEFAULT
// and then requires the key to match [A-Z][A-Z0-9_]*.
type HeartRateZoneSport struct {
	value string
}

// The two sport keys this package names: the generic fallback profile, and the alias
// upstream accepts for it.
const (
	heartRateZoneSportDefault = "DEFAULT"
	heartRateZoneSportGeneric = "GENERIC"
)

// heartRateZoneSportPattern is upstream's own accepted shape. A function, not a var:
// this package allows no package-level mutable state.
func heartRateZoneSportPattern() *regexp.Regexp {
	return regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
}

// normalizeZoneToken upper-cases and folds the separators upstream folds.
func normalizeZoneToken(value string) string {
	upper := strings.ToUpper(strings.TrimSpace(value))
	return strings.NewReplacer("-", "_", " ", "_").Replace(upper)
}

// ParseHeartRateZoneSport validates a caller-supplied sport key.
func ParseHeartRateZoneSport(value string) (HeartRateZoneSport, error) {
	normalized := normalizeZoneToken(value)
	if normalized == heartRateZoneSportGeneric {
		normalized = heartRateZoneSportDefault
	}
	if !heartRateZoneSportPattern().MatchString(normalized) {
		return HeartRateZoneSport{}, fmt.Errorf(
			"%w: sport must be a Garmin sport key such as DEFAULT, RUNNING or CYCLING",
			client.ErrValidation)
	}
	return HeartRateZoneSport{value: normalized}, nil
}

// DefaultHeartRateZoneSport is the generic profile every other sport inherits from.
func DefaultHeartRateZoneSport() HeartRateZoneSport {
	return HeartRateZoneSport{value: heartRateZoneSportDefault}
}

// IsZero reports whether the sport is unset.
func (s HeartRateZoneSport) IsZero() bool { return s.value == "" }

// String is the validated key, or "".
func (s HeartRateZoneSport) String() string { return s.value }

// HeartRateZoneMethod is a validated Garmin zone-calculation method.
//
// Garmin persists three values. CUSTOM_BPM is not one of them: upstream uses it as a
// local sentinel meaning "the explicit floors are authoritative", sends HR_MAX on the
// wire, and this package does the same. Source: _HEART_RATE_ZONE_METHODS and the
// trainingMethod branch of set_heart_rate_zones.
type HeartRateZoneMethod struct {
	value string
}

// The methods Garmin stores, plus the local custom sentinel.
const (
	HeartRateMethodMax       = "HR_MAX"
	HeartRateMethodReserve   = "HR_RESERVE"
	HeartRateMethodThreshold = "LACTATE_THRESHOLD"
	HeartRateMethodCustomBPM = "CUSTOM_BPM"
)

// heartRateZoneMethods is upstream's alias table, keyed by the normalized input.
func heartRateZoneMethods() map[string]string {
	return map[string]string{
		"HR_MAX":            HeartRateMethodMax,
		"MAX_HR":            HeartRateMethodMax,
		"MAXHR":             HeartRateMethodMax,
		"%MAX_HR":           HeartRateMethodMax,
		"HR_RESERVE":        HeartRateMethodReserve,
		"HRR":               HeartRateMethodReserve,
		"KARVONEN":          HeartRateMethodReserve,
		"%HRR":              HeartRateMethodReserve,
		"LACTATE_THRESHOLD": HeartRateMethodThreshold,
		"LTHR":              HeartRateMethodThreshold,
		"%LTHR":             HeartRateMethodThreshold,
		"CUSTOM":            HeartRateMethodCustomBPM,
		"CUSTOM_BPM":        HeartRateMethodCustomBPM,
		"BPM":               HeartRateMethodCustomBPM,
	}
}

// ParseHeartRateZoneMethod validates a caller-supplied calculation method.
func ParseHeartRateZoneMethod(value string) (HeartRateZoneMethod, error) {
	resolved, ok := heartRateZoneMethods()[normalizeZoneToken(value)]
	if !ok {
		return HeartRateZoneMethod{}, fmt.Errorf(
			"%w: calculation method must be max_hr, hrr, karvonen, lthr or custom_bpm",
			client.ErrValidation)
	}
	return HeartRateZoneMethod{value: resolved}, nil
}

// IsZero reports whether the method is unset.
func (m HeartRateZoneMethod) IsZero() bool { return m.value == "" }

// String is the resolved method, or "".
func (m HeartRateZoneMethod) String() string { return m.value }

// IsCustomBPM reports whether the method is the explicit-floors sentinel.
func (m HeartRateZoneMethod) IsCustomBPM() bool {
	return m.value == HeartRateMethodCustomBPM
}

// WireValue is the value Garmin is sent, which is HR_MAX for the custom sentinel
// because Garmin persists no custom method of its own.
func (m HeartRateZoneMethod) WireValue() string {
	if m.IsCustomBPM() {
		return HeartRateMethodMax
	}
	return m.value
}

// HeartRateZoneCount is how many zone floors Garmin stores per profile.
const HeartRateZoneCount = 5

// A HeartRateZoneProfile is one sport's zone configuration.
//
// It is health material: a heart-rate configuration describes a person's physiology,
// so it is never logged. Every optional field is a pointer, because the write is a
// read-modify-write: a field this package cannot see is a field it must send back
// unchanged rather than as a zero.
type HeartRateZoneProfile struct {
	Sport                 *string `json:"sport"`
	MaxHeartRate          *int    `json:"maxHeartRateUsed"`
	RestingHeartRate      *int    `json:"restingHeartRateUsed"`
	RestingAutoUpdate     *bool   `json:"restingHrAutoUpdateUsed"`
	LactateThresholdHeart *int    `json:"lactateThresholdHeartRateUsed"`
	TrainingMethod        *string `json:"trainingMethod"`

	Zone1Floor *int `json:"zone1Floor"`
	Zone2Floor *int `json:"zone2Floor"`
	Zone3Floor *int `json:"zone3Floor"`
	Zone4Floor *int `json:"zone4Floor"`
	Zone5Floor *int `json:"zone5Floor"`

	// ChangeState is the flag Garmin's write takes. It is never read from a caller:
	// SetHeartRateZones sets it.
	ChangeState *string `json:"changeState,omitempty"`
}

// Floors returns the five zone floors in order, each nil where Garmin holds none.
func (p HeartRateZoneProfile) Floors() [HeartRateZoneCount]*int {
	return [HeartRateZoneCount]*int{
		p.Zone1Floor, p.Zone2Floor, p.Zone3Floor, p.Zone4Floor, p.Zone5Floor,
	}
}

// WithFloors returns a copy carrying the given floors. The receiver is not modified.
func (p HeartRateZoneProfile) WithFloors(
	floors [HeartRateZoneCount]int,
) HeartRateZoneProfile {
	out := p
	out.Zone1Floor, out.Zone2Floor = &floors[0], &floors[1]
	out.Zone3Floor, out.Zone4Floor = &floors[2], &floors[3]
	out.Zone5Floor = &floors[4]
	return out
}

// changeStateChanged is the flag Garmin's write expects on a profile it should save.
const changeStateChanged = "CHANGED"

// HeartRateZones reads every saved zone profile.
//
// Garmin answers with an array; a shape this package cannot read is refused rather
// than passed on, which is what the shared list decoder does.
func (p *Profile) HeartRateZones(
	ctx context.Context, session client.Session,
) ([]HeartRateZoneProfile, error) {
	req := readRequest(client.OpGetHeartRateZones, client.EndpointHeartRateZones,
		client.PathHeartRateZones, nil)

	var list client.List[HeartRateZoneProfile]
	if _, err := p.req.read(ctx, session, req, &list); err != nil {
		return nil, err
	}
	return list.Items(), nil
}

// SetHeartRateZones writes one sport's zone profile.
//
// The profile is sent as the single element of an array, which is the shape Garmin's
// endpoint takes, and it carries changeState=CHANGED. It is EffectIdempotentWrite:
// writing the same configuration twice leaves the same end state, so the request layer
// may repeat it after a transport failure.
func (p *Profile) SetHeartRateZones(
	ctx context.Context, session client.Session, profile HeartRateZoneProfile,
) (WriteResult, error) {
	req := writeRequest(client.OpSetHeartRateZones, client.EndpointHeartRateZones,
		http.MethodPut, client.PathHeartRateZones, client.EffectIdempotentWrite)

	if profile.Sport == nil || *profile.Sport == "" {
		return WriteResult{}, invalid(req, fmt.Errorf(
			"%w: a sport key is required for a zone write", client.ErrValidation))
	}
	changed := profile
	state := changeStateChanged
	changed.ChangeState = &state

	body, err := jsonBody(req, []HeartRateZoneProfile{changed})
	if err != nil {
		return WriteResult{}, err
	}
	req.Body = body

	payload, err := p.req.write(ctx, session, req, nil)
	if err != nil {
		return WriteResult{}, err
	}
	return newWriteResult(payload), nil
}
