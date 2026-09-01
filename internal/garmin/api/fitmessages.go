package api

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/proto"
	"github.com/tamcore/garmin-mcp/internal/garmin/client"
)

// The generic FIT message inspection behind get_activity_fit_messages.
//
// fitdecode.go decodes the same container into the curated activity model this
// server's analysis reads. This file keeps every message and every field instead,
// uncurated, so a caller can inspect what a device actually wrote — a strength set, a
// shifting event, a manufacturer-specific message this server has no model for.
//
// Two things are deliberately not passed through:
//
//   - A coordinate. The FIT SDK decodes every field of every message, positions
//     included, and this server returns none of them anywhere. A position field is
//     reported by name with Suppressed set and no value, so a caller can see that the
//     device recorded one without this server handing over a route.
//   - An unreadable value. A field whose value does not decode as its own base type
//     is reported with no value rather than with a guess.
//
// Source: upstream's _parse_fit_messages and _serialize_fit_field
// (activity_analysis.py:297-520).

// FITSuppressedFieldSuffixes are the field-name endings of a coordinate. The FIT
// profile spells every position field with a lat or long suffix — position_lat,
// start_position_long, nec_lat, swc_long — so the suffix set covers the profile
// without naming each message.
func FITSuppressedFieldSuffixes() []string {
	return []string{"_lat", "_long", "_lon"}
}

// FITMessageSelection is what one inspection asks for.
//
// Types narrows the inspection to the named message types, matched case-insensitively
// against the FIT profile's own snake_case names. Empty means every type but the
// high-frequency record stream, which IncludeRecords adds back.
type FITMessageSelection struct {
	Types          []string
	IncludeRecords bool
	Offset         int
	Limit          int
}

// The bounds one inspection accepts. Source: upstream's own
// `if not 1 <= message_limit <= 5000` and `message_offset < 0` guards.
const (
	MinFITMessageLimit = 1
	MaxFITMessageLimit = 5000
)

// fitRecordType is the high-frequency per-second message every activity file is
// mostly made of.
const fitRecordType = "record"

// A FITFieldView is one decoded field of one message.
type FITFieldView struct {
	Name       string
	Number     int
	Value      any
	Units      string
	BaseType   string
	Suppressed bool
}

// A FITMessageView is one decoded message, with every field it carried.
type FITMessageView struct {
	Index     int
	TypeIndex int
	Type      string
	GlobalNum int
	Fields    []FITFieldView
}

// A FITMessageInventory is what one inspection produced.
//
// Counts covers the whole file regardless of the selection, so a caller learns what
// the file holds even when it asked for one type. Messages is the requested page of
// the selected stream, in file order.
type FITMessageInventory struct {
	Counts        map[string]int
	Messages      []FITMessageView
	TotalSelected int
	Offset        int
	Limit         int
	NextOffset    int
	HasMore       bool
	RecordCount   int
}

// InspectFITMessages decodes one activity file and reports its messages.
//
// data is what Garmin served for the original format: a zip archive holding the
// device FIT file, or the bare file. Both are accepted and both are bounded before
// anything is decoded, exactly as ParseFITActivity bounds them.
//
// The decode is streaming: only the requested page is retained, so the memory one
// inspection costs is set by the page rather than by the file.
func InspectFITMessages(
	ctx context.Context, data []byte, limits FITLimits, selection FITMessageSelection,
) (FITMessageInventory, error) {
	if selection.Offset < 0 {
		return FITMessageInventory{}, fmt.Errorf(
			"%w: the message offset must not be negative", client.ErrValidation)
	}
	if selection.Limit < MinFITMessageLimit || selection.Limit > MaxFITMessageLimit {
		return FITMessageInventory{}, fmt.Errorf(
			"%w: the message limit must be between %d and %d",
			client.ErrValidation, MinFITMessageLimit, MaxFITMessageLimit)
	}

	resolved := limits.withDefaults()
	raw, err := extractFIT(ctx, data, resolved)
	if err != nil {
		return FITMessageInventory{}, err
	}
	return decodeFITMessages(ctx, raw, resolved, selection)
}

// decodeFITMessages drives the SDK's decoder and keeps this server's bounds around it.
func decodeFITMessages(
	parent context.Context, raw []byte, limits FITLimits, selection FITMessageSelection,
) (FITMessageInventory, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	inspector := &fitInspector{
		limits:    limits,
		selection: selection,
		selected:  selectedFITTypes(selection.Types),
		counts:    map[string]int{},
		seen:      map[string]int{},
		stop:      cancel,
	}
	dec := decoder.New(bytes.NewReader(raw),
		decoder.WithMesgListener(inspector),
		decoder.WithBroadcastOnly())

	for dec.Next() {
		if _, err := dec.DecodeWithContext(ctx); err != nil {
			return FITMessageInventory{}, inspector.fail(parent.Err())
		}
	}
	if inspector.messages == 0 {
		return FITMessageInventory{}, fmt.Errorf("%w: the activity file carries no records",
			client.ErrMalformedPayload)
	}
	return inspector.inventory(), nil
}

// selectedFITTypes normalizes the requested type names. A nil result means every type.
func selectedFITTypes(types []string) map[string]struct{} {
	if len(types) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(types))
	for _, name := range types {
		trimmed := strings.ToLower(strings.TrimSpace(name))
		if trimmed == "" {
			continue
		}
		out[trimmed] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fitInspector receives every decoded message, counts it, and keeps the page.
type fitInspector struct {
	limits    FITLimits
	selection FITMessageSelection
	selected  map[string]struct{}
	stop      context.CancelFunc

	counts        map[string]int
	seen          map[string]int
	messages      int
	selectedCount int
	overflow      bool

	page []FITMessageView
}

// OnMesg receives one decoded message. It is the SDK's listener entry point, and it
// returns no error: a bound it cannot honour cancels the decode instead.
func (i *fitInspector) OnMesg(mesg proto.Message) {
	i.messages++
	if i.messages > i.limits.MaxMessages {
		i.overflow = true
		i.stop()
		return
	}

	name := mesg.Num.String()
	typeIndex := i.seen[name]
	i.counts[name]++
	i.seen[name] = typeIndex + 1

	if !i.isSelected(name) {
		return
	}
	index := i.selectedCount
	i.selectedCount++

	if index < i.selection.Offset || index >= i.selection.Offset+i.selection.Limit {
		return
	}
	i.page = append(i.page, FITMessageView{
		Index:     i.messages - 1,
		TypeIndex: typeIndex,
		Type:      name,
		GlobalNum: int(mesg.Num),
		Fields:    fitFieldViews(mesg.Fields),
	})
}

// isSelected reports whether one message type belongs to the requested stream.
func (i *fitInspector) isSelected(name string) bool {
	if name == fitRecordType && !i.selection.IncludeRecords {
		return false
	}
	if i.selected == nil {
		return true
	}
	_, requested := i.selected[strings.ToLower(name)]
	return requested
}

// inventory renders what the pass produced.
func (i *fitInspector) inventory() FITMessageInventory {
	out := FITMessageInventory{
		Counts:        i.counts,
		Messages:      i.page,
		TotalSelected: i.selectedCount,
		Offset:        i.selection.Offset,
		Limit:         i.selection.Limit,
		RecordCount:   i.counts[fitRecordType],
	}
	if next := i.selection.Offset + len(i.page); next < i.selectedCount {
		out.HasMore = true
		out.NextOffset = next
	}
	return out
}

// fail turns the recorded reason into the error the caller sees. It never quotes the
// file.
func (i *fitInspector) fail(cancelled error) error {
	if cancelled != nil {
		return fmt.Errorf("decoding the activity file: %w", cancelled)
	}
	if i.overflow {
		return fmt.Errorf("%w: the activity file carries more messages than this server decodes",
			client.ErrResponseTooLarge)
	}
	return fmt.Errorf("%w: the activity file is not a FIT file this server can decode",
		client.ErrMalformedPayload)
}

// fitFieldViews renders every field of one message, suppressing the coordinates.
func fitFieldViews(fields []proto.Field) []FITFieldView {
	out := make([]FITFieldView, 0, len(fields))
	for _, field := range fields {
		view := FITFieldView{
			Name:     fitFieldName(field),
			Number:   int(field.Num),
			Units:    field.Units,
			BaseType: field.BaseType.String(),
		}
		if isSuppressedFITField(view.Name) {
			view.Suppressed = true
			out = append(out, view)
			continue
		}
		if field.Value.Valid(field.BaseType) {
			view.Value = field.Value.Any()
		}
		out = append(out, view)
	}
	return out
}

// fitFieldName is the profile's name for one field, or its number when the profile
// names none. Source: upstream's `f"unknown_{definition_number}"` fallback.
func fitFieldName(field proto.Field) string {
	if field.Name != "" {
		return field.Name
	}
	return fmt.Sprintf("unknown_%d", field.Num)
}

// isSuppressedFITField reports whether one field name is a coordinate.
func isSuppressedFITField(name string) bool {
	lowered := strings.ToLower(name)
	for _, suffix := range FITSuppressedFieldSuffixes() {
		if strings.HasSuffix(lowered, suffix) {
			return true
		}
	}
	return false
}
