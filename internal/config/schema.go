package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/seat"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	colorFormat     = "lys-wm-color"
	fontFileFormat  = "lys-wm-font-file"
	iconThemeFormat = "lys-wm-icon-theme"
	layoutFormat    = "lys-wm-xkb-layout"
	variantFormat   = "lys-wm-xkb-variant"
	optionsFormat   = "lys-wm-xkb-options"
	modelFormat     = "lys-wm-xkb-model"
	rulesFormat     = "lys-wm-xkb-rules"
	drmDeviceFormat = "lys-wm-drm-device"
	regexFormat     = "regex"
	actionFormat    = "lys-wm-action"
	chordFormat     = "lys-wm-chord"
)

type schemaNode struct {
	Schema               string                 `json:"$schema,omitempty"`
	ID                   string                 `json:"$id,omitempty"`
	Type                 string                 `json:"type,omitempty"`
	Format               string                 `json:"format,omitempty"`
	Enum                 []string               `json:"enum,omitempty"`
	Minimum              *float64               `json:"minimum,omitempty"`
	ExclusiveMinimum     *float64               `json:"exclusiveMinimum,omitempty"`
	Maximum              *float64               `json:"maximum,omitempty"`
	MinItems             *int                   `json:"minItems,omitempty"`
	MaxItems             *int                   `json:"maxItems,omitempty"`
	Default              any                    `json:"default,omitempty"`
	OneOf                []*schemaNode          `json:"oneOf,omitempty"`
	Items                *schemaNode            `json:"items,omitempty"`
	Properties           map[string]*schemaNode `json:"properties,omitempty"`
	AdditionalProperties any                    `json:"additionalProperties,omitempty"`
	Actions              []schemaAction         `json:"x-lys-wm-actions,omitempty"`
}

type annotation struct {
	format             string
	actions            bool
	enum               []string
	minimum, maximum   *float64
	exclusiveMinimum   *float64
	minItems, maxItems *int
	def                any
}

func at(lo, hi float64, def any) annotation {
	return annotation{minimum: new(lo), maximum: new(hi), def: def}
}

func above(lo, hi float64, def any) annotation {
	return annotation{exclusiveMinimum: new(lo), maximum: new(hi), def: def}
}

func atLeastAnn(lo float64, def any) annotation {
	return annotation{minimum: new(lo), def: def}
}

func colorAnn(def any) annotation { return annotation{format: colorFormat, def: def} }

func formatAnn(format string) annotation { return annotation{format: format} }

var borderColors = annotation{minItems: new(1), maxItems: new(compositor.MaxBorderColors)}

func sidesValue(in wm.Insets) any {
	if in.Top == in.Right && in.Top == in.Bottom && in.Top == in.Left {
		return in.Top
	}
	return []int{in.Top, in.Right, in.Bottom, in.Left}
}

func builtInPalette() map[string]string {
	out := make(map[string]string, len(mocha))
	for name, c := range mocha {
		out[name] = hexColor(c)
	}
	return out
}

var bindFlags = annotation{enum: []string{"locked", "repeating"}}

var actionAnn = annotation{format: actionFormat, actions: true}

type schemaAction struct {
	Name    string `json:"name"`
	Args    string `json:"args,omitempty"`
	Summary string `json:"summary"`
}

func schemaActions() []schemaAction {
	out := make([]schemaAction, len(wm.Actions))
	for i, a := range wm.Actions {
		out[i] = schemaAction{Name: a.Name, Args: a.Args, Summary: a.Summary}
	}
	return out
}

func hexColor(c render.Color) string {
	if c.A == 0xff {
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A)
}

func short(v float32) float64 {
	f, err := strconv.ParseFloat(strconv.FormatFloat(float64(v), 'g', -1, 32), 64)
	if err != nil {
		panic(fmt.Sprintf("config: a float32 did not format as a float: %v", err))
	}
	return f
}

func device(prefix string) map[string]annotation {
	return map[string]annotation{
		prefix + ".click_method":  {enum: slices.Sorted(maps.Keys(clickMethods))},
		prefix + ".scroll_method": {enum: slices.Sorted(maps.Keys(scrollMethods))},
		prefix + ".accel_profile": {enum: slices.Sorted(maps.Keys(accelProfiles))},
		prefix + ".accel_speed":   at(-1, 1, nil),
		prefix + ".scroll_factor": above(0, 100, nil),
	}
}

func lookAnnotations(prefix string, saturation any) map[string]annotation {
	return map[string]annotation{
		prefix + ".tint":       colorAnn(nil),
		prefix + ".saturation": at(0, 4, saturation),
		prefix + ".brightness": at(0, 4, 1.0),
		prefix + ".contrast":   at(0, 4, 1.0),
		prefix + ".grain":      at(0, 1, 0.0),
	}
}

func annotations() map[string]annotation {
	look := compositor.DefaultLook(render.GPU, compositor.DefaultAccent)
	d := look.Decor
	s := compositor.DefaultSettings()
	out := map[string]annotation{
		"input.keyboard.repeat_rate":       at(0, math.MaxInt32, seat.DefaultRepeatRate),
		"input.keyboard.repeat_delay":      at(0, math.MaxInt32, seat.DefaultRepeatDelay),
		"input.keyboard.slow_keys":         at(0, math.MaxInt32, 0),
		"input.keyboard.sticky_keys":       {def: false},
		"borders.width":                    atLeastAnn(0, d.BorderWidth),
		"gaps.inner":                       atLeastAnn(0, s.Gaps.Inner),
		"gaps.outer":                       {def: sidesValue(s.Gaps.Outer)},
		"borders.active":                   {def: accentName},
		"borders.inactive":                 {def: hexColor(d.InactiveBorder.Colors()[0])},
		"borders.active.colors":            borderColors,
		"borders.inactive.colors":          borderColors,
		"palette":                          {def: builtInPalette()},
		"decoration.rounding":              atLeastAnn(0, d.Rounding),
		"decoration.active_opacity":        at(0, 1, short(d.Opacity.Active)),
		"decoration.inactive_opacity":      at(0, 1, short(d.Opacity.Inactive)),
		"decoration.fullscreen_opacity":    at(0, 1, short(d.Opacity.Fullscreen)),
		"decoration.dim_inactive":          {def: d.Dim.Inactive},
		"decoration.dim_strength":          at(0, 1, short(d.Dim.Strength)),
		"decoration.shadow.enabled":        {def: d.Shadow.Enabled},
		"decoration.shadow.range":          atLeastAnn(0, d.Shadow.Range),
		"decoration.shadow.render_power":   at(1, 4, d.Shadow.Power),
		"decoration.shadow.sharp":          {def: d.Shadow.Sharp},
		"decoration.shadow.scale":          above(0, 1, d.Shadow.Scale),
		"decoration.shadow.color":          colorAnn(hexColor(d.Shadow.Active)),
		"decoration.shadow.color_inactive": colorAnn(hexColor(d.Shadow.Inactive)),
		"decoration.blur.enabled":          {def: d.Blur.Enabled},
		"decoration.blur.size":             atLeastAnn(1, d.Blur.Size),
		"decoration.blur.passes":           at(1, 4, d.Blur.Passes),
		"decoration.blur.xray":             {def: d.Blur.XRay},
		"decoration.blur.live_tiled":       {def: d.Blur.LiveTiled},
		"drag_preview.enabled":             {def: d.DragPreview.Enabled},
		"drag_preview.opacity":             at(0, 1, short(d.DragPreview.Opacity)),
		"drag_preview.blur":                {def: d.DragPreview.Blur},
		"drag_preview.snap_distance":       atLeastAnn(1, d.DragPreview.SnapDistance),
		"animations.enabled":               {def: true},
		"animations.reduced_motion":        {def: false},
		"overview.blur":                    {def: look.Overview.Blur},
		"overview.dim":                     at(0, 1, short(look.Overview.Dim)),
		"tabs.height":                      atLeastAnn(1, d.Tab.Height),
		"tabs.padding":                     atLeastAnn(0, d.Tab.Padding),
		"tabs.text_height":                 atLeastAnn(1, d.Tab.TextHeight),
		"tabs.text_padding":                atLeastAnn(0, d.Tab.TextPadding),
		"autotile.enabled":                 {def: s.Autotile.Enabled},
		"autotile.ephemeral":               {def: s.Autotile.Ephemeral},
		"autotile.min_trigger":             atLeastAnn(0, s.Autotile.MinTrigger),
		"autotile.divisor":                 atLeastAnn(1, s.Autotile.Divisor),
		"gestures.distance":                {exclusiveMinimum: new(0.0), def: compositor.DefaultGestures().Distance},
		"xwayland.enabled":                 {def: true},
		"xwayland.zero_scaling":            {def: true},
		"color_management":                 {def: false},
		"client_hotkeys":                   {def: false},
		"hidden_frame_rate":                atLeastAnn(1, int(time.Second/compositor.DefaultHiddenFrame)),
		"power_saver_frame_rate":           atLeastAnn(0, compositor.DefaultPowerSaverRate),
		"hold_opening":                     {def: false},
		"tearing":                          {def: false},
		"lid_suspend":                      {def: true},
		"gpu":                              formatAnn(drmDeviceFormat),
		"tabs.font":                        formatAnn(fontFileFormat),
		"tabs.icon_theme":                  formatAnn(iconThemeFormat),
		"input.keyboard.layout":            formatAnn(layoutFormat),
		"input.keyboard.variant":           formatAnn(variantFormat),
		"input.keyboard.options":           formatAnn(optionsFormat),
		"input.keyboard.model":             formatAnn(modelFormat),
		"input.keyboard.rules":             formatAnn(rulesFormat),
		"decoration.blur.mica.tint":        colorAnn(nil),
		"rules[].blur_look.tint":           colorAnn(nil),
		"rules[].app_id":                   formatAnn(regexFormat),
		"rules[].title":                    formatAnn(regexFormat),
		"rules[].tag":                      formatAnn(regexFormat),
		"outputs[].vrr":                    {enum: slices.Sorted(maps.Keys(vrrPolicies))},
		"focus.on_activate":                {enum: slices.Sorted(maps.Keys(focusActivations)), def: "focus"},
		"fullscreen.new_window":            {enum: slices.Sorted(maps.Keys(coverPolicies)), def: "behind"},
		"rules[].on_activate":              {enum: slices.Sorted(maps.Keys(activations))},
		"rules[].content":                  {enum: slices.Sorted(maps.Keys(contentTypes))},
		"binds[].flags[]":                  bindFlags,
		"submaps.*[].flags[]":              bindFlags,
		"binds[].chord":                    formatAnn(chordFormat),
		"submaps.*[].chord":                formatAnn(chordFormat),
		"binds[].action":                   actionAnn,
		"submaps.*[].action":               actionAnn,
		"gestures.swipe[].action":          actionAnn,
		"gestures.pinch[].action":          actionAnn,
		"gestures.hold[].action":           actionAnn,
	}
	for _, state := range []string{"active", "inactive", "urgent"} {
		out["tabs."+state+".background"] = colorAnn(nil)
		out["tabs."+state+".text"] = colorAnn(nil)
	}
	saturation := short(d.Blur.Look.SaturationShift + 1)
	for _, group := range []map[string]annotation{
		device("input.pointer"),
		device("input.touchpad"),
		device("input.devices.*"),
		lookAnnotations("decoration.blur", saturation),
	} {
		maps.Copy(out, group)
	}
	return out
}

var (
	sidesType  = reflect.TypeFor[sides]()
	borderType = reflect.TypeFor[rawBorder]()
)

func schemaFor(t reflect.Type, path string, notes map[string]annotation) *schemaNode {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if reflect.PointerTo(t).Implements(locatedType) {
		t = t.Field(0).Type
	}
	n := &schemaNode{}
	switch {
	case t == sidesType:
		n.OneOf = []*schemaNode{{Type: "integer"}, {Type: "array", Items: &schemaNode{Type: "integer"}}}
	case t == borderType:
		object := objectSchema(t, path, notes)
		n.OneOf = []*schemaNode{{Type: "string", Format: colorFormat}, object}
	case t.Kind() == reflect.Struct:
		n = objectSchema(t, path, notes)
	case t.Kind() == reflect.Map:
		n.Type = "object"
		n.AdditionalProperties = schemaFor(t.Elem(), path+".*", notes)
	case t.Kind() == reflect.Slice:
		n.Type = "array"
		n.Items = schemaFor(t.Elem(), path+"[]", notes)
	case t.Kind() == reflect.Bool:
		n.Type = "boolean"
	case t.Kind() == reflect.Int:
		n.Type = "integer"
	case t.Kind() == reflect.Float64:
		n.Type = "number"
	case t.Kind() == reflect.String:
		n.Type = "string"
	default:
		panic(fmt.Sprintf("config: %s has a %s, which the schema does not describe", path, t))
	}
	if note, ok := notes[path]; ok {
		n.Format, n.Enum, n.Minimum, n.Maximum, n.ExclusiveMinimum, n.Default, n.MinItems, n.MaxItems = note.format, note.enum, note.minimum, note.maximum, note.exclusiveMinimum, note.def, note.minItems, note.maxItems
		if note.actions {
			n.Actions = schemaActions()
		}
	}
	return n
}

func objectSchema(t reflect.Type, path string, notes map[string]annotation) *schemaNode {
	fields := fieldNames(t)
	n := &schemaNode{Type: "object", AdditionalProperties: false, Properties: make(map[string]*schemaNode, len(fields))}
	for name, ft := range fields {
		child := name
		if path != "" {
			child = path + "." + name
		}
		n.Properties[name] = schemaFor(ft, child, notes)
	}
	return n
}

func Schema() []byte {
	root := schemaFor(reflect.TypeFor[rawConfig](), "", annotations())
	root.Schema = "https://json-schema.org/draft/2020-12/schema"
	root.ID = "https://github.com/stubbedev/lys-wm/blob/main/schema/lys-wm-config.schema.json"
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		panic(fmt.Sprintf("config: encode the schema: %v", err))
	}
	return out.Bytes()
}
