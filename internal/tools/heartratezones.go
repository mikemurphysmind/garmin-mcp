package tools

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tamcore/garmin-mcp/internal/garmin/api"
	"github.com/tamcore/garmin-mcp/internal/mcpserver"
	"github.com/tamcore/garmin-mcp/internal/policy"
)

// The two heart-rate zone tools. Both are additions beyond the pinned manifest:
// upstream added them after the pinned commit. They share one endpoint and one result
// model, so they share a file.
const (
	ToolGetHeartRateZones = "get_heart_rate_zones"
	ToolSetHeartRateZones = "set_heart_rate_zones"
)

// The bounds every heart-rate argument is validated against. Source: the numeric
// checks of upstream's _validate_heart_rate_zone_config, which accept 1 to 300 bpm.
const (
	minHeartRateArgument = 1
	maxHeartRateArgument = 300
)

// The argument names and argument bounds the two tools take.
const (
	argSport              = "sport"
	argMaxHR              = "max_hr"
	argRestingHR          = "resting_hr"
	argLactateThresholdHR = "lactate_threshold_hr"
	argCalculationMethod  = "calculation_method"
	argZoneBoundaries     = "zone_boundaries"

	// maxSportArgumentLen and maxMethodArgumentLen bound the two string arguments.
	// Garmin's own keys are short, and an unbounded string is a log hazard before it
	// ever reaches validation.
	maxSportArgumentLen  = 32
	maxMethodArgumentLen = 24

	// zoneBoundariesRequired is how many floors Garmin stores per profile.
	zoneBoundariesRequired = api.HeartRateZoneCount
)

// A HeartRateZoneConfig is one sport's zone configuration.
//
// It is health data — never log it, never cache it. The zone floors are reported as a
// list in zone order, which is how a caller sets them.
type HeartRateZoneConfig struct {
	Sport string `json:"sport" jsonschema:"Garmin's sport key, e.g. DEFAULT"`

	MaxHeartRateBPM     *int    `json:"max_hr_bpm,omitempty" jsonschema:"the maximum rate"`
	RestingHeartRateBPM *int    `json:"resting_hr_bpm,omitempty" jsonschema:"the resting rate"`
	RestingAutoUpdate   *bool   `json:"resting_hr_auto_update,omitempty" jsonschema:"auto-updated"`
	LactateThresholdBPM *int    `json:"lactate_threshold_hr_bpm,omitempty" jsonschema:"threshold rate"`
	CalculationMethod   *string `json:"calculation_method,omitempty" jsonschema:"Garmin's method"`
	ZoneBoundariesBPM   []int   `json:"zone_boundaries_bpm,omitempty" jsonschema:"the zone floors"`
}

// A HeartRateZoneList is every saved profile, or the one profile that was asked for.
type HeartRateZoneList struct {
	Zones []HeartRateZoneConfig `json:"zones" jsonschema:"the saved zone profiles"`
	Count int                   `json:"count" jsonschema:"how many profiles this carries"`
	Note  string                `json:"note,omitempty" jsonschema:"why none is reported"`
}

// LogValue reports how many profiles exist, never a reading.
func (l HeartRateZoneList) LogValue() slog.Value {
	return shape("heartRateZoneList", slog.Int("zones", len(l.Zones)))
}

// A HeartRateZoneUpdate is the configuration Garmin saved, re-read after the write.
type HeartRateZoneUpdate struct {
	Sport   string              `json:"sport" jsonschema:"the sport that was written"`
	Applied bool                `json:"applied" jsonschema:"whether Garmin reported it back"`
	Zone    HeartRateZoneConfig `json:"zone" jsonschema:"the configuration Garmin saved"`
	Note    string              `json:"note,omitempty" jsonschema:"why it could not be re-read"`
}

// LogValue reports the outcome, never a reading.
func (u HeartRateZoneUpdate) LogValue() slog.Value {
	return shape("heartRateZoneUpdate", slog.Bool("applied", u.Applied))
}

// heartRateZonesInput is the read's argument set: an optional sport filter.
type heartRateZonesInput struct {
	Sport *string `json:"sport,omitempty" jsonschema:"a sport key; omit for every profile"`
}

// setHeartRateZonesInput is the write's argument set. Every value but the sport is
// optional: an omitted field keeps the value the sport's current profile carries.
type setHeartRateZonesInput struct {
	Sport              string  `json:"sport" jsonschema:"the Garmin sport key to write"`
	MaxHR              *int    `json:"max_hr,omitempty" jsonschema:"the maximum rate, bpm"`
	RestingHR          *int    `json:"resting_hr,omitempty" jsonschema:"the resting rate, bpm"`
	LactateThresholdHR *int    `json:"lactate_threshold_hr,omitempty" jsonschema:"threshold, bpm"`
	CalculationMethod  *string `json:"calculation_method,omitempty" jsonschema:"how zones derive"`
	ZoneBoundaries     []int   `json:"zone_boundaries,omitempty" jsonschema:"five bpm floors"`
}

func getHeartRateZonesContract() Contract {
	return Contract{
		Spec: mcpserver.ToolSpec{
			Name:  ToolGetHeartRateZones,
			Title: "Get the heart-rate zones",
			Description: "read the account's saved heart-rate training zones. Garmin " +
				"stores a generic DEFAULT profile plus optional sport-specific overrides " +
				"such as RUNNING and CYCLING: with no sport this returns every saved " +
				"profile, and with one it returns just that profile. The sport key " +
				"generic is accepted for DEFAULT",
			Tier:        policy.TierReadOnly,
			Category:    categoryHealth,
			Annotations: readOnlyAnnotations(),
		},
		Schema: NewSchema(sportProperty(false)),
	}
}

func setHeartRateZonesContract() Contract {
	return Contract{
		Spec: mcpserver.ToolSpec{
			Name:  ToolSetHeartRateZones,
			Title: "Set the heart-rate zones",
			Description: "write one sport's heart-rate training zones on the account. " +
				"Garmin stores the profiles independently, so only the requested sport is " +
				"written and an omitted value is read from that sport's current profile " +
				"and preserved. The change affects future recording only: it never " +
				"re-slices activities already recorded. Manual zone_boundaries need " +
				"calculation_method=custom_bpm, which Garmin stores as HR_MAX with the " +
				"explicit floors. The result is the profile Garmin saved, re-read after " +
				"the write, rather than the request",
			Tier:        policy.TierWrite,
			Category:    categoryHealth,
			Annotations: writeAnnotations(true),
		},
		Schema: NewSchema(
			sportProperty(true),
			heartRateProperty(argMaxHR, "the maximum heart rate, bpm"),
			heartRateProperty(argRestingHR,
				"the resting heart rate, bpm; supplying it stops Garmin's own updates"),
			heartRateProperty(argLactateThresholdHR, "the lactate-threshold rate, bpm"),
			Property{
				Name:        argCalculationMethod,
				Types:       []string{typeString},
				Description: "how Garmin derives the zones",
				Enum:        []any{"max_hr", "hrr", "karvonen", "lthr", "custom_bpm"},
				MaxLength:   new(maxMethodArgumentLen),
			},
			Property{
				Name:        argZoneBoundaries,
				Types:       []string{typeArray},
				Description: "the five zone floors in bpm, strictly increasing",
				MinItems:    new(zoneBoundariesRequired),
				MaxItems:    new(zoneBoundariesRequired),
				Items: map[string]any{
					keyType:    typeInteger,
					keyMinimum: minHeartRateArgument,
					keyMaximum: maxHeartRateArgument,
				},
			},
		),
	}
}

// sportProperty declares the sport argument both tools take.
func sportProperty(required bool) Property {
	return Property{
		Name:        argSport,
		Types:       []string{typeString},
		Description: "a Garmin sport key such as DEFAULT, RUNNING or CYCLING",
		MaxLength:   new(maxSportArgumentLen),
		Required:    required,
	}
}

// heartRateProperty declares one bounded heart-rate argument.
func heartRateProperty(name, description string) Property {
	return Property{
		Name:        name,
		Types:       []string{typeInteger},
		Description: description,
		Minimum:     bound(minHeartRateArgument),
		Maximum:     bound(maxHeartRateArgument),
	}
}

// registerGetHeartRateZones registers the read.
func registerGetHeartRateZones(registry *mcpserver.Registry, svc *service) error {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in heartRateZonesInput) (
		*mcp.CallToolResult, HeartRateZoneList, error,
	) {
		var filter api.HeartRateZoneSport
		if in.Sport != nil {
			parsed, err := api.ParseHeartRateZoneSport(*in.Sport)
			if err != nil {
				return nil, HeartRateZoneList{}, invalidArgument(argSport +
					" must be a Garmin sport key such as DEFAULT, RUNNING or CYCLING")
			}
			filter = parsed
		}

		session, err := svc.session(ctx)
		if err != nil {
			return nil, HeartRateZoneList{}, err
		}
		profiles, err := svc.profile.HeartRateZones(ctx, session)
		if err != nil {
			return nil, HeartRateZoneList{}, fail(err)
		}
		return nil, newHeartRateZoneList(profiles, filter), nil
	}
	return mcpserver.AddTool(registry, getHeartRateZonesContract().Registration(), handler)
}

// registerSetHeartRateZones registers the write.
func registerSetHeartRateZones(registry *mcpserver.Registry, svc *service) error {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in setHeartRateZonesInput) (
		*mcp.CallToolResult, HeartRateZoneUpdate, error,
	) {
		out, err := svc.setHeartRateZones(ctx, in)
		return nil, out, err
	}
	return mcpserver.AddTool(registry, setHeartRateZonesContract().Registration(), handler)
}

// setHeartRateZones performs the read-modify-write behind the tool.
//
// The current profile is read first, so an omitted argument keeps the value Garmin
// already holds; a sport with no profile of its own inherits DEFAULT's, exactly as
// upstream does. The merged profile is validated before it is sent, and the saved
// profile is re-read afterwards so the result is what Garmin stored rather than what
// was asked for.
func (s *service) setHeartRateZones(
	ctx context.Context, in setHeartRateZonesInput,
) (HeartRateZoneUpdate, error) {
	request, err := parseHeartRateZoneRequest(in)
	if err != nil {
		return HeartRateZoneUpdate{}, err
	}
	session, err := s.session(ctx)
	if err != nil {
		return HeartRateZoneUpdate{}, err
	}

	profiles, err := s.profile.HeartRateZones(ctx, session)
	if err != nil {
		return HeartRateZoneUpdate{}, fail(err)
	}
	merged, err := mergeHeartRateZoneProfile(profiles, request)
	if err != nil {
		return HeartRateZoneUpdate{}, err
	}
	if _, err := s.profile.SetHeartRateZones(ctx, session, merged); err != nil {
		return HeartRateZoneUpdate{}, fail(err)
	}

	saved, err := s.profile.HeartRateZones(ctx, session)
	if err != nil {
		return HeartRateZoneUpdate{}, fail(err)
	}
	out := HeartRateZoneUpdate{Sport: request.sport.String()}
	if written, ok := findHeartRateZone(saved, request.sport); ok {
		out.Applied = true
		out.Zone = newHeartRateZoneConfig(written)
		return out, nil
	}
	out.Note = "Garmin accepted the write but reported no profile for this sport on " +
		"read-back, so what it stored could not be confirmed"
	return out, nil
}

// A heartRateZoneRequest is the validated write request.
type heartRateZoneRequest struct {
	sport       api.HeartRateZoneSport
	maxHR       *int
	restingHR   *int
	thresholdHR *int
	method      api.HeartRateZoneMethod
	boundaries  []int
}

// parseHeartRateZoneRequest validates the arguments at the boundary, before any Garmin
// call is made.
func parseHeartRateZoneRequest(in setHeartRateZonesInput) (heartRateZoneRequest, error) {
	sport, err := api.ParseHeartRateZoneSport(in.Sport)
	if err != nil {
		return heartRateZoneRequest{}, invalidArgument(argSport +
			" must be a Garmin sport key such as DEFAULT, RUNNING or CYCLING")
	}
	out := heartRateZoneRequest{
		sport:       sport,
		maxHR:       in.MaxHR,
		restingHR:   in.RestingHR,
		thresholdHR: in.LactateThresholdHR,
		boundaries:  in.ZoneBoundaries,
	}

	if in.CalculationMethod != nil {
		method, methodErr := api.ParseHeartRateZoneMethod(*in.CalculationMethod)
		if methodErr != nil {
			return heartRateZoneRequest{}, invalidArgument(argCalculationMethod +
				" must be max_hr, hrr, karvonen, lthr or custom_bpm")
		}
		out.method = method
	}

	if err := validateHeartRateZoneRequest(out); err != nil {
		return heartRateZoneRequest{}, err
	}
	return out, nil
}

// validateHeartRateZoneRequest applies the rules that hold before the current profile
// is known.
func validateHeartRateZoneRequest(request heartRateZoneRequest) error {
	if request.maxHR == nil && request.restingHR == nil && request.thresholdHR == nil &&
		request.method.IsZero() && len(request.boundaries) == 0 {
		return invalidArgument("supply at least one of " + argMaxHR + ", " + argRestingHR +
			", " + argLactateThresholdHR + ", " + argCalculationMethod + " or " +
			argZoneBoundaries)
	}
	for name, value := range map[string]*int{
		argMaxHR:              request.maxHR,
		argRestingHR:          request.restingHR,
		argLactateThresholdHR: request.thresholdHR,
	} {
		if value != nil && (*value < minHeartRateArgument || *value > maxHeartRateArgument) {
			return invalidArgument(name + " must be between " +
				strconv.Itoa(minHeartRateArgument) + " and " +
				strconv.Itoa(maxHeartRateArgument) + " bpm")
		}
	}
	return validateZoneBoundaries(request)
}

// validateZoneBoundaries applies the rules that bind the floors to the method.
func validateZoneBoundaries(request heartRateZoneRequest) error {
	if len(request.boundaries) > 0 && !request.method.IsCustomBPM() {
		return invalidArgument("manual " + argZoneBoundaries + " need " +
			argCalculationMethod + "=custom_bpm")
	}
	if request.method.IsCustomBPM() && len(request.boundaries) == 0 {
		return invalidArgument(argCalculationMethod + "=custom_bpm needs all " +
			strconv.Itoa(zoneBoundariesRequired) + " " + argZoneBoundaries)
	}
	if len(request.boundaries) == 0 {
		return nil
	}
	if len(request.boundaries) != zoneBoundariesRequired {
		return invalidArgument(argZoneBoundaries + " must hold exactly " +
			strconv.Itoa(zoneBoundariesRequired) + " floors")
	}
	for index, floor := range request.boundaries {
		if floor < minHeartRateArgument || floor > maxHeartRateArgument {
			return invalidArgument(argZoneBoundaries + " must hold bpm values between " +
				strconv.Itoa(minHeartRateArgument) + " and " +
				strconv.Itoa(maxHeartRateArgument))
		}
		if index > 0 && floor <= request.boundaries[index-1] {
			return invalidArgument(argZoneBoundaries + " must increase strictly")
		}
	}
	return nil
}

// mergeHeartRateZoneProfile folds the request onto the sport's current profile, or onto
// DEFAULT's when the sport has none of its own, and validates the result.
func mergeHeartRateZoneProfile(
	profiles []api.HeartRateZoneProfile, request heartRateZoneRequest,
) (api.HeartRateZoneProfile, error) {
	merged, ok := findHeartRateZone(profiles, request.sport)
	if !ok {
		inherited, hasDefault := findHeartRateZone(profiles, api.DefaultHeartRateZoneSport())
		if !hasDefault {
			return api.HeartRateZoneProfile{}, incompleteProfile(
				"it holds no zone profile for this sport and no DEFAULT profile to " +
					"inherit from, so the update cannot be applied")
		}
		merged = inherited
		sport := request.sport.String()
		merged.Sport = &sport
	}

	if request.maxHR != nil {
		merged.MaxHeartRate = request.maxHR
	}
	if request.restingHR != nil {
		merged.RestingHeartRate = request.restingHR
		autoUpdate := false
		merged.RestingAutoUpdate = &autoUpdate
	}
	if request.thresholdHR != nil {
		merged.LactateThresholdHeart = request.thresholdHR
	}
	if !request.method.IsZero() {
		wire := request.method.WireValue()
		merged.TrainingMethod = &wire
	}
	if len(request.boundaries) == zoneBoundariesRequired {
		var floors [api.HeartRateZoneCount]int
		copy(floors[:], request.boundaries)
		merged = merged.WithFloors(floors)
	}

	if err := validateMergedHeartRateZones(merged); err != nil {
		return api.HeartRateZoneProfile{}, err
	}
	return merged, nil
}

// validateMergedHeartRateZones applies the rules that need the whole profile: the
// checks upstream's _validate_heart_rate_zone_config makes after the merge.
func validateMergedHeartRateZones(profile api.HeartRateZoneProfile) error {
	maxHR := profile.MaxHeartRate
	if maxHR == nil || *maxHR < minHeartRateArgument || *maxHR > maxHeartRateArgument {
		return invalidArgument("the current or supplied " + argMaxHR +
			" must be between " + strconv.Itoa(minHeartRateArgument) + " and " +
			strconv.Itoa(maxHeartRateArgument) + " bpm")
	}
	if resting := profile.RestingHeartRate; resting != nil && *resting >= *maxHR {
		return invalidArgument(argRestingHR + " must be lower than " + argMaxHR)
	}
	if threshold := profile.LactateThresholdHeart; threshold != nil && *threshold > *maxHR {
		return invalidArgument(argLactateThresholdHR + " must not exceed " + argMaxHR)
	}

	previous := 0
	for index, floor := range profile.Floors() {
		if floor == nil {
			return invalidArgument("the merged profile holds no floor for zone " +
				strconv.Itoa(index+1) + ", so " + argZoneBoundaries + " must be supplied")
		}
		if *floor < minHeartRateArgument || *floor > *maxHR {
			return invalidArgument(argZoneBoundaries +
				" must hold bpm values that do not exceed " + argMaxHR)
		}
		if index > 0 && *floor <= previous {
			return invalidArgument(argZoneBoundaries + " must increase strictly")
		}
		previous = *floor
	}
	return nil
}

// findHeartRateZone returns the profile of one sport.
func findHeartRateZone(
	profiles []api.HeartRateZoneProfile, sport api.HeartRateZoneSport,
) (api.HeartRateZoneProfile, bool) {
	for _, profile := range profiles {
		if profile.Sport != nil && *profile.Sport == sport.String() {
			return profile, true
		}
	}
	return api.HeartRateZoneProfile{}, false
}

// newHeartRateZoneList curates the profiles, keeping only the requested sport when one
// was named.
func newHeartRateZoneList(
	profiles []api.HeartRateZoneProfile, filter api.HeartRateZoneSport,
) HeartRateZoneList {
	out := HeartRateZoneList{Zones: []HeartRateZoneConfig{}}
	for _, profile := range profiles {
		if !filter.IsZero() && (profile.Sport == nil || *profile.Sport != filter.String()) {
			continue
		}
		out.Zones = append(out.Zones, newHeartRateZoneConfig(profile))
	}
	out.Count = len(out.Zones)
	if out.Count == 0 {
		out.Note = "Garmin holds no configured heart-rate zones for this request"
	}
	return out
}

// newHeartRateZoneConfig curates one profile.
func newHeartRateZoneConfig(profile api.HeartRateZoneProfile) HeartRateZoneConfig {
	out := HeartRateZoneConfig{
		Sport:               stringOrEmpty(profile.Sport),
		MaxHeartRateBPM:     profile.MaxHeartRate,
		RestingHeartRateBPM: profile.RestingHeartRate,
		RestingAutoUpdate:   profile.RestingAutoUpdate,
		LactateThresholdBPM: profile.LactateThresholdHeart,
		CalculationMethod:   profile.TrainingMethod,
	}
	for _, floor := range profile.Floors() {
		if floor == nil {
			continue
		}
		out.ZoneBoundariesBPM = append(out.ZoneBoundariesBPM, *floor)
	}
	return out
}
