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

// ToolGetActivityFITMessages is the upstream compatibility name of the generic FIT
// inspection. It is an addition beyond the pinned manifest: upstream added it after
// the pinned commit.
const ToolGetActivityFITMessages = "get_activity_fit_messages"

// The page bounds this tool accepts. The two page bounds are the api layer's own,
// restated so the schema can declare them.
const (
	defaultFITMessageLimit = 1000
	maxFITMessageTypes     = 64
	maxFITMessageOffset    = 1_000_000
	maxFITTypeNameLen      = 64
)

// The argument names this tool takes.
const (
	argMessageTypes   = "message_types"
	argMessageOffset  = "message_offset"
	argMessageLimit   = "message_limit"
	argIncludeRecords = "include_records"
)

// A FITMessageField is one decoded field of one message.
//
// A position field is reported by name with suppressed set and no value: the device
// recorded a coordinate, and this server returns none.
type FITMessageField struct {
	Name       string `json:"name" jsonschema:"the FIT profile's field name"`
	Number     int    `json:"definition_number" jsonschema:"the field's profile number"`
	Value      any    `json:"value,omitempty" jsonschema:"the decoded value, when readable"`
	Units      string `json:"units,omitempty" jsonschema:"the profile's unit, when it has one"`
	BaseType   string `json:"base_type,omitempty" jsonschema:"the field's FIT base type"`
	Suppressed bool   `json:"suppressed,omitzero" jsonschema:"whether the value is withheld"`
}

// A FITMessage is one decoded message in file order.
type FITMessage struct {
	Index     int               `json:"message_index" jsonschema:"its position in the file"`
	TypeIndex int               `json:"type_index" jsonschema:"its position among its own type"`
	Type      string            `json:"type" jsonschema:"the FIT message type name"`
	GlobalNum int               `json:"global_message_number" jsonschema:"the profile number"`
	Fields    []FITMessageField `json:"fields" jsonschema:"every field the message carried"`
}

// FITMessagePage is the pagination envelope of one inspection.
type FITMessagePage struct {
	TotalSelected int  `json:"total_selected" jsonschema:"how many messages the selection holds"`
	ReturnedCount int  `json:"returned_count" jsonschema:"how many this page carries"`
	Offset        int  `json:"offset" jsonschema:"the offset this page starts at"`
	Limit         int  `json:"limit" jsonschema:"the page size that was applied"`
	NextOffset    *int `json:"next_offset,omitempty" jsonschema:"the next offset, when more remain"`
}

// FITMessages is the whole inspection of one activity file.
//
// It is device material: a message stream says what a person did second by second, so
// it is never logged. Coordinates are suppressed field by field.
type FITMessages struct {
	ActivityID int    `json:"activity_id" jsonschema:"the activity the file belongs to"`
	FileBytes  int    `json:"fit_size_bytes" jsonschema:"how many bytes the file carried"`
	Source     string `json:"source" jsonschema:"which file this was decoded from"`

	MessageCounts   map[string]int `json:"message_counts" jsonschema:"every type, with its count"`
	RecordCount     int            `json:"record_count" jsonschema:"how many records the file holds"`
	RecordsIncluded bool           `json:"records_included" jsonschema:"whether records are here"`

	Pagination FITMessagePage `json:"pagination" jsonschema:"the page this result carries"`
	Messages   []FITMessage   `json:"messages" jsonschema:"the selected messages, in file order"`
}

// LogValue reports the shape of the inspection, never a field value.
func (m FITMessages) LogValue() slog.Value {
	return shape("fitMessages",
		slog.Int("types", len(m.MessageCounts)),
		slog.Int("returned", m.Pagination.ReturnedCount),
		slog.Bool("more", m.Pagination.NextOffset != nil),
	)
}

// fitMessagesInput is the strict argument set.
type fitMessagesInput struct {
	ActivityID     any      `json:"activity_id" jsonschema:"the Garmin activity identifier"`
	MessageTypes   []string `json:"message_types,omitempty" jsonschema:"the FIT types to return"`
	IncludeRecords *bool    `json:"include_records,omitempty" jsonschema:"include the records"`
	MessageOffset  *int     `json:"message_offset,omitempty" jsonschema:"the page offset"`
	MessageLimit   *int     `json:"message_limit,omitempty" jsonschema:"the page size"`
}

// fitMessagesSource names the file the inspection decoded, so a caller knows the
// result is the device's own file rather than Garmin's service-side view of it.
const fitMessagesSource = "garmin_original_fit"

func getActivityFITMessagesContract() Contract {
	return Contract{
		Spec: mcpserver.ToolSpec{
			Name:  ToolGetActivityFITMessages,
			Title: "Get an activity's FIT messages",
			Description: "download one activity's original device FIT file and return its " +
				"messages without sport-specific curation: every message type the file " +
				"holds with its count, and the requested page of the selected messages, " +
				"each field with its value, unit, profile number and base type. Use " +
				"message_types to select particular types — a strength session reads well " +
				"as session, lap, set and exercise_title — and follow " +
				"pagination.next_offset until it is absent. The per-second record stream is " +
				"counted but returned only when include_records is set. Coordinate fields " +
				"are reported by name and marked suppressed: this server returns no " +
				"positions. Garmin Connect edits made after upload live in Garmin's service " +
				"and are not necessarily written back into this file",
			Tier:        policy.TierReadOnly,
			Category:    categoryLocation,
			Annotations: readOnlyAnnotations(),
		},
		Schema: NewSchema(
			activityIDProperty(),
			Property{
				Name:        argMessageTypes,
				Types:       []string{typeArray},
				Description: "the FIT message types to return; omit for every type but records",
				MaxItems:    new(maxFITMessageTypes),
				Items: map[string]any{
					keyType:      typeString,
					keyMaxLength: maxFITTypeNameLen,
				},
			},
			Property{
				Name:        argIncludeRecords,
				Types:       []string{typeBoolean},
				Description: "include the per-second record stream, which is large",
				Default:     false,
			},
			Property{
				Name:        argMessageOffset,
				Types:       []string{typeInteger},
				Description: "the zero-based offset within the selected stream",
				Minimum:     bound(0),
				Maximum:     bound(maxFITMessageOffset),
				Default:     0,
			},
			Property{
				Name:        argMessageLimit,
				Types:       []string{typeInteger},
				Description: "how many messages this page may carry",
				Minimum:     bound(api.MinFITMessageLimit),
				Maximum:     bound(api.MaxFITMessageLimit),
				Default:     defaultFITMessageLimit,
			},
		),
	}
}

// registerGetActivityFITMessages registers the tool.
func registerGetActivityFITMessages(registry *mcpserver.Registry, svc *service) error {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in fitMessagesInput) (
		*mcp.CallToolResult, FITMessages, error,
	) {
		out, err := svc.activityFITMessages(ctx, in)
		if err != nil {
			return nil, FITMessages{}, err
		}
		return nil, out, nil
	}
	return mcpserver.AddTool(registry,
		getActivityFITMessagesContract().Registration(), handler)
}

// activityFITMessages downloads one activity file and inspects it.
//
// The file is streamed into memory under the download bound, exactly as
// get_activity_fit_data streams it, and the decode keeps only the requested page.
func (s *service) activityFITMessages(
	ctx context.Context, in fitMessagesInput,
) (FITMessages, error) {
	id, session, err := s.resolveActivityRead(ctx, in.ActivityID)
	if err != nil {
		return FITMessages{}, err
	}
	selection, err := parseFITMessageSelection(in)
	if err != nil {
		return FITMessages{}, err
	}

	sink := newBoundedSink(s.bounds.MaxDownloadBytes)
	_, transferErr := s.files.Download(ctx, session, id, api.FormatOriginal, sink)
	// The sink is asked first: it aborts the copy, so its own refusal is the cause of
	// the transfer error and is the one worth reporting.
	if sinkErr := sink.err(); sinkErr != nil {
		return FITMessages{}, sinkErr
	}
	if transferErr != nil {
		return FITMessages{}, fail(transferErr)
	}

	inventory, err := api.InspectFITMessages(ctx, sink.bytes(), fitLimits(), selection)
	if err != nil {
		return FITMessages{}, fail(err)
	}
	return newFITMessages(int(id.Int64()), sink.len(), selection, inventory), nil
}

// parseFITMessageSelection validates the page arguments at the boundary, before the
// file is downloaded.
func parseFITMessageSelection(in fitMessagesInput) (api.FITMessageSelection, error) {
	selection := api.FITMessageSelection{
		Types:          in.MessageTypes,
		IncludeRecords: in.IncludeRecords != nil && *in.IncludeRecords,
		Limit:          defaultFITMessageLimit,
	}
	if in.MessageOffset != nil {
		selection.Offset = *in.MessageOffset
	}
	if in.MessageLimit != nil {
		selection.Limit = *in.MessageLimit
	}

	switch {
	case selection.Offset < 0 || selection.Offset > maxFITMessageOffset:
		return api.FITMessageSelection{}, invalidArgument(argMessageOffset +
			" must be between 0 and " + strconv.Itoa(maxFITMessageOffset))
	case selection.Limit < api.MinFITMessageLimit || selection.Limit > api.MaxFITMessageLimit:
		return api.FITMessageSelection{}, invalidArgument(argMessageLimit +
			" must be between " + strconv.Itoa(api.MinFITMessageLimit) + " and " +
			strconv.Itoa(api.MaxFITMessageLimit))
	case len(selection.Types) > maxFITMessageTypes:
		return api.FITMessageSelection{}, invalidArgument(argMessageTypes +
			" must name at most " + strconv.Itoa(maxFITMessageTypes) + " types")
	}
	for _, name := range selection.Types {
		if len(name) > maxFITTypeNameLen {
			return api.FITMessageSelection{}, invalidArgument("each " + argMessageTypes +
				" entry must be at most " + strconv.Itoa(maxFITTypeNameLen) + " characters")
		}
	}
	return selection, nil
}

// newFITMessages renders the inspection.
func newFITMessages(
	activityID, size int, selection api.FITMessageSelection,
	inventory api.FITMessageInventory,
) FITMessages {
	out := FITMessages{
		ActivityID:      activityID,
		FileBytes:       size,
		Source:          fitMessagesSource,
		MessageCounts:   inventory.Counts,
		RecordCount:     inventory.RecordCount,
		RecordsIncluded: selection.IncludeRecords,
		Pagination: FITMessagePage{
			TotalSelected: inventory.TotalSelected,
			ReturnedCount: len(inventory.Messages),
			Offset:        inventory.Offset,
			Limit:         inventory.Limit,
		},
		Messages: []FITMessage{},
	}
	if inventory.HasMore {
		next := inventory.NextOffset
		out.Pagination.NextOffset = &next
	}
	if out.MessageCounts == nil {
		out.MessageCounts = map[string]int{}
	}

	for _, message := range inventory.Messages {
		fields := make([]FITMessageField, 0, len(message.Fields))
		for _, field := range message.Fields {
			fields = append(fields, FITMessageField{
				Name:       field.Name,
				Number:     field.Number,
				Value:      field.Value,
				Units:      field.Units,
				BaseType:   field.BaseType,
				Suppressed: field.Suppressed,
			})
		}
		out.Messages = append(out.Messages, FITMessage{
			Index:     message.Index,
			TypeIndex: message.TypeIndex,
			Type:      message.Type,
			GlobalNum: message.GlobalNum,
			Fields:    fields,
		})
	}
	return out
}
