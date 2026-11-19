package scene

import (
	"math"
	"regexp"
	"unicode/utf8"
)

// Skeleton is a simplified element description, modelled on
// ExcalidrawElementSkeleton (packages/element/src/transform.ts) since that is
// the format models already know for generating Excalidraw diagrams.
type Skeleton struct {
	ID   string `json:"id,omitempty" jsonschema:"optional id (letters, digits, _ or -), so other elements in this or later calls can refer to it; generated if omitted"`
	Type string `json:"type" jsonschema:"one of: rectangle, ellipse, diamond, text, arrow, line, frame"`

	X      *float64 `json:"x,omitempty" jsonschema:"left edge (top-left origin, y grows downwards); required except for arrows given start and end, and frames that should fit their children"`
	Y      *float64 `json:"y,omitempty" jsonschema:"top edge"`
	Width  *float64 `json:"width,omitempty" jsonschema:"rectangle/ellipse/diamond/frame width; shapes with a label fit the label when omitted, otherwise 100"`
	Height *float64 `json:"height,omitempty" jsonschema:"rectangle/ellipse/diamond/frame height"`

	Text     string   `json:"text,omitempty" jsonschema:"content of a text element; use \\n for line breaks"`
	FontSize *float64 `json:"fontSize,omitempty" jsonschema:"text size in px, default 20 (16 small, 28 large, 36 very large)"`
	// TextAlign applies to text elements only.
	TextAlign  string `json:"textAlign,omitempty" jsonschema:"text elements: left (default), center or right"`
	FontFamily string `json:"fontFamily,omitempty" jsonschema:"hand (default, hand-drawn), normal or code"`

	Label *Label `json:"label,omitempty" jsonschema:"text centred inside a rectangle/ellipse/diamond, or on an arrow"`

	Points         [][2]float64 `json:"points,omitempty" jsonschema:"arrow/line points relative to x,y, e.g. [[0,0],[200,0]]; not needed for an arrow with both start and end"`
	Start          *Ref         `json:"start,omitempty" jsonschema:"arrow: element the arrow starts at (it stays attached when that element moves)"`
	End            *Ref         `json:"end,omitempty" jsonschema:"arrow: element the arrow ends at"`
	StartArrowhead string       `json:"startArrowhead,omitempty" jsonschema:"arrow: none (default), arrow, bar, circle, circle_outline, triangle, triangle_outline, diamond, diamond_outline, or ER cardinality_one, cardinality_many, cardinality_one_or_many, cardinality_exactly_one, cardinality_zero_or_one, cardinality_zero_or_many"`
	EndArrowhead   string       `json:"endArrowhead,omitempty" jsonschema:"arrow: arrow (default) or any startArrowhead value"`

	StrokeColor     string   `json:"strokeColor,omitempty" jsonschema:"hex color, default #1e1e1e; palette: #1e1e1e #e03131 #2f9e44 #1971c2 #f08c00 #6741d9"`
	BackgroundColor string   `json:"backgroundColor,omitempty" jsonschema:"hex color or transparent (default); light palette: #ffc9c9 #b2f2bb #a5d8ff #ffec99 #d0bfff #e9ecef"`
	FillStyle       string   `json:"fillStyle,omitempty" jsonschema:"solid (default), hachure, cross-hatch or zigzag"`
	StrokeWidth     *float64 `json:"strokeWidth,omitempty" jsonschema:"1 thin, 2 (default), 4 bold"`
	StrokeStyle     string   `json:"strokeStyle,omitempty" jsonschema:"solid (default), dashed or dotted"`
	Roughness       *int     `json:"roughness,omitempty" jsonschema:"0 architect (clean lines), 1 (default, sketchy), 2 cartoonist"`
	Opacity         *int     `json:"opacity,omitempty" jsonschema:"0-100, default 100"`
	Roundness       string   `json:"roundness,omitempty" jsonschema:"round (default for rectangle, diamond, arrow, line) or sharp"`

	Name    string `json:"name,omitempty" jsonschema:"frame title"`
	FrameID string `json:"frameId,omitempty" jsonschema:"id of a frame (existing or in this call) that contains this element"`
}

type Label struct {
	Text        string   `json:"text" jsonschema:"label text; long text wraps to the shape width"`
	FontSize    *float64 `json:"fontSize,omitempty" jsonschema:"default 20"`
	StrokeColor string   `json:"strokeColor,omitempty" jsonschema:"text color, defaults to the shape's stroke color"`
}

type Ref struct {
	ID string `json:"id" jsonschema:"id of an existing element or of one created in the same call"`
}

const (
	MaxElementsPerCall = 500
	maxTextRunes       = 10_000
	maxCoordinate      = 1e6
	maxPoints          = 1000
)

var (
	idPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	colorPattern = regexp.MustCompile(`^(transparent|#[0-9a-fA-F]{3}|#[0-9a-fA-F]{6}|#[0-9a-fA-F]{8})$`)

	shapeTypes   = map[string]bool{"rectangle": true, "ellipse": true, "diamond": true}
	elementTypes = map[string]bool{"rectangle": true, "ellipse": true, "diamond": true, "text": true, "arrow": true, "line": true, "frame": true}
	// Arrowhead in packages/element/src/types.ts (current names, not legacy ones)
	arrowheads = map[string]bool{
		"none": true, "arrow": true, "bar": true,
		"circle": true, "circle_outline": true, "triangle": true, "triangle_outline": true,
		"diamond": true, "diamond_outline": true,
		"cardinality_one": true, "cardinality_many": true, "cardinality_one_or_many": true,
		"cardinality_exactly_one": true, "cardinality_zero_or_one": true, "cardinality_zero_or_many": true,
	}
	fillStyles   = map[string]bool{"solid": true, "hachure": true, "cross-hatch": true, "zigzag": true}
	strokeStyles = map[string]bool{"solid": true, "dashed": true, "dotted": true}
	textAligns   = map[string]bool{"left": true, "center": true, "right": true}
	// FONT_FAMILY in packages/common/src/constants.ts: the editor's three
	// default picks
	fontFamilies = map[string]int{"": 5, "hand": 5, "normal": 6, "code": 8}
)

func validCoord(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= maxCoordinate
}

func checkCoord(path, field string, v *float64) error {
	if v != nil && !validCoord(*v) {
		return inputErr("%s.%s must be a finite number within ±%g", path, field, maxCoordinate)
	}
	return nil
}

func checkSize(path, field string, v *float64) error {
	if err := checkCoord(path, field, v); err != nil {
		return err
	}
	if v != nil && *v <= 0 {
		return inputErr("%s.%s must be positive", path, field)
	}
	return nil
}

func checkText(path, text string) error {
	if !utf8.ValidString(text) {
		return inputErr("%s must be valid UTF-8", path)
	}
	if utf8.RuneCountInString(text) > maxTextRunes {
		return inputErr("%s is longer than %d characters", path, maxTextRunes)
	}
	return nil
}

func checkFontSize(path string, v *float64) error {
	if v != nil && (math.IsNaN(*v) || *v < 4 || *v > 512) {
		return inputErr("%s.fontSize must be between 4 and 512", path)
	}
	return nil
}

func checkColor(path, field, v string) error {
	if v != "" && !colorPattern.MatchString(v) {
		return inputErr("%s.%s must be a hex color like #1e1e1e or transparent", path, field)
	}
	return nil
}

func checkEnum(path, field, v string, allowed map[string]bool) error {
	if v != "" && !allowed[v] {
		return inputErr("%s.%s: unsupported value %q", path, field, v)
	}
	return nil
}

// validate checks one skeleton in isolation; references between elements are
// checked by the builder, which knows the scene.
func (sk *Skeleton) validate(path string) error {
	if !elementTypes[sk.Type] {
		return inputErr("%s.type: unsupported type %q (use rectangle, ellipse, diamond, text, arrow, line or frame)", path, sk.Type)
	}
	if sk.ID != "" && !idPattern.MatchString(sk.ID) {
		return inputErr("%s.id must be 1-64 letters, digits, _ or -", path)
	}
	for field, v := range map[string]*float64{"x": sk.X, "y": sk.Y} {
		if err := checkCoord(path, field, v); err != nil {
			return err
		}
	}
	for field, v := range map[string]*float64{"width": sk.Width, "height": sk.Height} {
		if err := checkSize(path, field, v); err != nil {
			return err
		}
	}
	isLinear := sk.Type == "arrow" || sk.Type == "line"
	bothEnds := sk.Type == "arrow" && sk.Start != nil && sk.End != nil
	// frames without a position fit their children, like the skeleton API
	if !bothEnds && sk.Type != "frame" && (sk.X == nil || sk.Y == nil) {
		return inputErr("%s needs x and y", path)
	}
	if isLinear && !bothEnds {
		if len(sk.Points) < 2 {
			return inputErr("%s needs at least 2 points (or start and end for an arrow)", path)
		}
	}
	if len(sk.Points) > maxPoints {
		return inputErr("%s has more than %d points", path, maxPoints)
	}
	for i, p := range sk.Points {
		if !validCoord(p[0]) || !validCoord(p[1]) {
			return inputErr("%s.points[%d] must be finite numbers within ±%g", path, i, maxCoordinate)
		}
	}
	if !isLinear && len(sk.Points) > 0 {
		return inputErr("%s: points only apply to arrows and lines", path)
	}
	if sk.Type != "arrow" && (sk.Start != nil || sk.End != nil || sk.StartArrowhead != "" || sk.EndArrowhead != "") {
		return inputErr("%s: start, end and arrowheads only apply to arrows", path)
	}
	for field, ref := range map[string]*Ref{"start": sk.Start, "end": sk.End} {
		if ref != nil && ref.ID == "" {
			return inputErr("%s.%s.id is required", path, field)
		}
	}
	if sk.Type == "text" {
		if sk.Text == "" {
			return inputErr("%s.text is required for text elements", path)
		}
	} else if sk.Text != "" {
		return inputErr("%s: use label (not text) to put text in a %s", path, sk.Type)
	}
	if err := checkText(path+".text", sk.Text); err != nil {
		return err
	}
	if sk.Label != nil {
		if !shapeTypes[sk.Type] && sk.Type != "arrow" {
			return inputErr("%s: labels only apply to rectangles, ellipses, diamonds and arrows", path)
		}
		if sk.Label.Text == "" {
			return inputErr("%s.label.text is required", path)
		}
		if err := checkText(path+".label.text", sk.Label.Text); err != nil {
			return err
		}
		if err := checkFontSize(path+".label", sk.Label.FontSize); err != nil {
			return err
		}
		if err := checkColor(path+".label", "strokeColor", sk.Label.StrokeColor); err != nil {
			return err
		}
	}
	if err := checkFontSize(path, sk.FontSize); err != nil {
		return err
	}
	if _, ok := fontFamilies[sk.FontFamily]; !ok {
		return inputErr("%s.fontFamily must be hand, normal or code", path)
	}
	if err := checkEnum(path, "textAlign", sk.TextAlign, textAligns); err != nil {
		return err
	}
	if err := checkEnum(path, "startArrowhead", sk.StartArrowhead, arrowheads); err != nil {
		return err
	}
	if err := checkEnum(path, "endArrowhead", sk.EndArrowhead, arrowheads); err != nil {
		return err
	}
	if err := checkEnum(path, "fillStyle", sk.FillStyle, fillStyles); err != nil {
		return err
	}
	if err := checkEnum(path, "strokeStyle", sk.StrokeStyle, strokeStyles); err != nil {
		return err
	}
	if err := checkEnum(path, "roundness", sk.Roundness, map[string]bool{"round": true, "sharp": true}); err != nil {
		return err
	}
	if err := checkColor(path, "strokeColor", sk.StrokeColor); err != nil {
		return err
	}
	if err := checkColor(path, "backgroundColor", sk.BackgroundColor); err != nil {
		return err
	}
	if sk.StrokeWidth != nil && (math.IsNaN(*sk.StrokeWidth) || *sk.StrokeWidth < 0.5 || *sk.StrokeWidth > 16) {
		return inputErr("%s.strokeWidth must be between 0.5 and 16", path)
	}
	if sk.Roughness != nil && (*sk.Roughness < 0 || *sk.Roughness > 2) {
		return inputErr("%s.roughness must be 0, 1 or 2", path)
	}
	if sk.Opacity != nil && (*sk.Opacity < 0 || *sk.Opacity > 100) {
		return inputErr("%s.opacity must be 0-100", path)
	}
	if sk.Name != "" && sk.Type != "frame" {
		return inputErr("%s: name only applies to frames", path)
	}
	if err := checkText(path+".name", sk.Name); err != nil {
		return err
	}
	if sk.FrameID != "" && sk.Type == "frame" {
		return inputErr("%s: frames cannot be nested", path)
	}
	return nil
}
