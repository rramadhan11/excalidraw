package scene

import (
	"fmt"
	"math"
)

// bindable lists the element types an arrow may attach to here.
var bindable = map[string]bool{"rectangle": true, "ellipse": true, "diamond": true, "text": true}

// Add builds elements from skeletons and appends them to the scene, binding
// arrows to their start/end elements (new or existing). It returns the ids of
// the elements the caller described, in order (labels are not listed).
// Nothing is changed if any skeleton is invalid.
func (s *Scene) Add(skeletons []Skeleton) ([]string, error) {
	if len(skeletons) == 0 {
		return nil, inputErr("elements is empty")
	}
	if len(skeletons) > MaxElementsPerCall {
		return nil, inputErr("at most %d elements per call", MaxElementsPerCall)
	}
	ids := make([]string, len(skeletons))
	index := make(map[string]int, len(skeletons))
	for i := range skeletons {
		sk := &skeletons[i]
		path := fmt.Sprintf("elements[%d]", i)
		if err := sk.validate(path); err != nil {
			return nil, err
		}
		id := sk.ID
		if id == "" {
			id = s.ed.newID()
		} else if _, exists := s.byID[id]; exists {
			return nil, inputErr("%s.id %q is already used on this board", path, id)
		} else if _, dup := index[id]; dup {
			return nil, inputErr("%s.id %q is used twice", path, id)
		}
		ids[i] = id
		index[id] = i
	}

	b := &builder{scene: s, skeletons: skeletons, ids: ids, index: index,
		built: make(map[string]Element, len(skeletons))}
	if err := b.checkReferences(); err != nil {
		return nil, err
	}
	// shapes, text and lines first: arrows and frames depend on their geometry
	for i := range skeletons {
		switch skeletons[i].Type {
		case "arrow", "frame":
		default:
			b.buildNonArrow(i)
		}
	}
	for i := range skeletons {
		if skeletons[i].Type == "arrow" {
			if err := b.buildArrow(i); err != nil {
				return nil, err
			}
		}
	}
	for i := range skeletons {
		if skeletons[i].Type == "frame" {
			if err := b.buildFrame(i); err != nil {
				return nil, err
			}
		}
	}

	s.touchAll(b.touched)
	// labels go right after their container, as repairBoundTextElementOrder
	// would put them
	for _, id := range ids {
		s.append(b.built[id])
		if label, ok := b.labels[id]; ok {
			s.append(label)
		}
	}
	return ids, nil
}

type builder struct {
	scene     *Scene
	skeletons []Skeleton
	ids       []string
	index     map[string]int
	built     map[string]Element
	labels    map[string]Element
	// existing elements that gained a bound arrow
	touched map[string]Element
}

// checkReferences validates start/end/frameId targets before anything is built.
func (b *builder) checkReferences() error {
	for i := range b.skeletons {
		sk := &b.skeletons[i]
		path := fmt.Sprintf("elements[%d]", i)
		for field, ref := range map[string]*Ref{"start": sk.Start, "end": sk.End} {
			if ref == nil {
				continue
			}
			typ, ok := b.refType(ref.ID)
			if !ok {
				return inputErr("%s.%s.id %q does not exist", path, field, ref.ID)
			}
			if !bindable[typ] {
				return inputErr("%s.%s.id %q is a %s; arrows attach to rectangles, ellipses, diamonds and text", path, field, ref.ID, typ)
			}
		}
		if sk.Start != nil && sk.End != nil && sk.Start.ID == sk.End.ID {
			return inputErr("%s: start and end must be different elements", path)
		}
		if sk.FrameID != "" {
			typ, ok := b.refType(sk.FrameID)
			if !ok || typ != "frame" {
				return inputErr("%s.frameId %q is not a frame on this board", path, sk.FrameID)
			}
		}
	}
	return nil
}

// refType resolves an id to a new skeleton or a live existing element.
func (b *builder) refType(id string) (string, bool) {
	if i, ok := b.index[id]; ok {
		return b.skeletons[i].Type, true
	}
	if el, ok := b.scene.live(id); ok {
		return el.Type(), true
	}
	return "", false
}

// target returns the element an arrow binds to: just built or existing.
func (b *builder) target(id string) Element {
	if el, ok := b.built[id]; ok {
		return el
	}
	el, _ := b.scene.live(id)
	return el
}

func orDefault(v *float64, def float64) float64 {
	if v != nil {
		return *v
	}
	return def
}

// base mirrors _newElementBase (packages/element/src/newElement.ts) with
// DEFAULT_ELEMENT_PROPS (packages/common/src/constants.ts).
func (b *builder) base(sk *Skeleton, id, typ string, x, y, width, height float64) Element {
	ed := b.scene.ed
	now := ed.timestamp()
	el := Element{
		"id":              id,
		"type":            typ,
		"x":               x,
		"y":               y,
		"width":           width,
		"height":          height,
		"angle":           0,
		"strokeColor":     "#1e1e1e",
		"backgroundColor": "transparent",
		"fillStyle":       "solid",
		"strokeWidth":     2,
		"strokeStyle":     "solid",
		"roughness":       1,
		"opacity":         100,
		"groupIds":        []any{},
		"frameId":         nil,
		"index":           nil,
		"roundness":       nil,
		"seed":            ed.randomInteger(),
		"version":         1,
		"versionNonce":    0,
		"isDeleted":       false,
		"boundElements":   []any{},
		"updated":         now,
		"created":         now,
		"link":            nil,
		"locked":          false,
	}
	if sk == nil {
		return el
	}
	if sk.StrokeColor != "" {
		el["strokeColor"] = sk.StrokeColor
	}
	if sk.BackgroundColor != "" {
		el["backgroundColor"] = sk.BackgroundColor
	}
	if sk.FillStyle != "" {
		el["fillStyle"] = sk.FillStyle
	}
	if sk.StrokeWidth != nil {
		el["strokeWidth"] = *sk.StrokeWidth
	}
	if sk.StrokeStyle != "" {
		el["strokeStyle"] = sk.StrokeStyle
	}
	if sk.Roughness != nil {
		el["roughness"] = *sk.Roughness
	}
	if sk.Opacity != nil {
		el["opacity"] = *sk.Opacity
	}
	if sk.FrameID != "" {
		el["frameId"] = sk.FrameID
	}
	// ROUNDNESS (packages/common/src/constants.ts): the editor's default
	// "round" edges are ADAPTIVE_RADIUS for rectangles and
	// PROPORTIONAL_RADIUS for diamonds and linear elements
	if sk.Roundness != "sharp" {
		switch typ {
		case "rectangle":
			el["roundness"] = map[string]any{"type": 3}
		case "diamond", "arrow", "line":
			el["roundness"] = map[string]any{"type": 2}
		}
	}
	return el
}

// newText mirrors newTextElement: x/y is the anchor for the alignment.
func (b *builder) newText(id, text string, fontSize float64, fontFamily int) Element {
	text = normalizeText(text)
	m := measure(text, fontSize, fontFamily)
	el := b.base(nil, id, "text", 0, 0, m.width, m.height)
	el["text"] = text
	el["originalText"] = text
	el["fontSize"] = fontSize
	el["fontFamily"] = fontFamily
	el["textAlign"] = "left"
	el["verticalAlign"] = "top"
	el["containerId"] = nil
	el["autoResize"] = true
	el["lineHeight"] = textLineHeight
	el["baseFontSize"] = nil
	el["labelPosition"] = nil
	return el
}

func (b *builder) buildNonArrow(i int) {
	sk := &b.skeletons[i]
	id := b.ids[i]
	x, y := orDefault(sk.X, 0), orDefault(sk.Y, 0)

	switch sk.Type {
	case "text":
		text := b.newText(id, sk.Text, orDefault(sk.FontSize, defaultFontSize), fontFamilies[sk.FontFamily])
		copyStyle(text, b.base(sk, id, "text", 0, 0, 0, 0), "strokeColor", "opacity", "frameId")
		width := text.num("width")
		align := sk.TextAlign
		if align == "" {
			align = "left"
		}
		text["textAlign"] = align
		// getTextElementPositionOffsets: x/y is where the aligned edge sits
		switch align {
		case "center":
			x -= width / 2
		case "right":
			x -= width
		}
		text["x"], text["y"] = x, y
		b.built[id] = text

	case "line":
		el := b.base(sk, id, "line", 0, 0, 0, 0)
		setLinearGeometry(el, x, y, sk.Points)
		el["startBinding"] = nil
		el["endBinding"] = nil
		el["startArrowhead"] = nil
		el["endArrowhead"] = nil
		el["polygon"] = false
		b.built[id] = el

	default: // rectangle, ellipse, diamond
		width := orDefault(sk.Width, defaultShapeSize)
		height := orDefault(sk.Height, defaultShapeSize)
		el := b.base(sk, id, sk.Type, x, y, width, height)
		b.built[id] = el
		if sk.Label != nil {
			b.addLabel(el, sk, sk.Width == nil, sk.Height == nil)
		}
	}
}

func copyStyle(dst, src Element, keys ...string) {
	for _, k := range keys {
		dst[k] = src[k]
	}
}

// addLabel creates the container's bound text, growing the container to fit
// it like redrawTextBoundingBox does. fitWidth/fitHeight size the container
// to the text when the caller gave no size.
func (b *builder) addLabel(container Element, sk *Skeleton, fitWidth, fitHeight bool) {
	fontSize := orDefault(sk.Label.FontSize, defaultFontSize)
	fontFamily := fontFamilies[sk.FontFamily]
	typ := container.Type()
	if fitWidth {
		natural := measure(normalizeText(sk.Label.Text), fontSize, fontFamily)
		container["width"] = math.Max(containerDimensionForText(natural.width, typ), 40)
	}
	label := b.newText(b.scene.ed.newID(), "", fontSize, fontFamily)
	label["originalText"] = normalizeText(sk.Label.Text)
	label["containerId"] = container.ID()
	label["textAlign"] = "center"
	label["verticalAlign"] = "middle"
	label["frameId"] = container["frameId"]
	label["opacity"] = container["opacity"]
	label["strokeColor"] = container["strokeColor"]
	if sk.Label.StrokeColor != "" {
		label["strokeColor"] = sk.Label.StrokeColor
	}
	container.setBoundRefs(append(container.boundRefs(), boundRef{Type: "text", ID: label.ID()}))
	if b.labels == nil {
		b.labels = map[string]Element{}
	}
	b.labels[container.ID()] = label
	layoutLabel(container, label, fitHeight)
}

// layoutLabel wraps the label to its container and centres it, growing the
// container's height when the text doesn't fit (or fitting it exactly when
// fitHeight). Arrow labels sit on the arrow's midpoint.
func layoutLabel(container, label Element, fitHeight bool) {
	fontSize := label.num("fontSize")
	fontFamily := int(label.num("fontFamily"))
	typ := container.Type()
	width := container.num("width")
	text := wrap(label.str("originalText"), boundTextMaxWidth(typ, width, fontSize), fontSize, fontFamily)
	m := measure(text, fontSize, fontFamily)
	label["text"] = text
	label["width"], label["height"] = m.width, m.height

	if typ == "arrow" {
		mx, my := arrowLabelAnchor(container)
		label["x"], label["y"] = mx-m.width/2, my-m.height/2
		return
	}
	needed := containerDimensionForText(m.height, typ)
	if fitHeight || container.num("height") < needed {
		container["height"] = math.Max(needed, 40)
	}
	cx, cy := container.num("x"), container.num("y")
	ox, oy := containerTextOrigin(typ, cx, cy, width, container.num("height"))
	maxW := boundTextMaxWidth(typ, width, fontSize)
	maxH := boundTextMaxHeight(typ, container.num("height"))
	label["x"] = ox + (maxW-m.width)/2
	label["y"] = oy + (maxH-m.height)/2
}

// arrowLabelAnchor mirrors LinearElementEditor.getBoundTextElementPosition:
// the middle point, or the middle of the middle segment.
func arrowLabelAnchor(arrow Element) (float64, float64) {
	pts := points(arrow)
	x, y := arrow.num("x"), arrow.num("y")
	n := len(pts)
	if n == 0 {
		return x, y
	}
	if n%2 == 1 {
		p := pts[n/2]
		return x + p[0], y + p[1]
	}
	a, c := pts[n/2-1], pts[n/2]
	return x + (a[0]+c[0])/2, y + (a[1]+c[1])/2
}

// points reads a linear element's relative points.
func points(el Element) [][2]float64 {
	list, _ := el["points"].([]any)
	out := make([][2]float64, 0, len(list))
	for _, item := range list {
		switch p := item.(type) {
		case []any:
			if len(p) == 2 {
				out = append(out, [2]float64{toFloat(p[0]), toFloat(p[1])})
			}
		case [2]float64:
			out = append(out, p)
		}
	}
	return out
}

// setLinearGeometry stores absolute-anchored points the way the editor keeps
// them: the first point is [0, 0] at (x, y), and width/height are the points'
// extent (getSizeFromPoints).
func setLinearGeometry(el Element, x, y float64, pts [][2]float64) {
	ox, oy := pts[0][0], pts[0][1]
	list := make([]any, len(pts))
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i, p := range pts {
		px, py := p[0]-ox, p[1]-oy
		list[i] = []any{px, py}
		minX, maxX = math.Min(minX, px), math.Max(maxX, px)
		minY, maxY = math.Min(minY, py), math.Max(maxY, py)
	}
	el["x"], el["y"] = x+ox, y+oy
	el["points"] = list
	el["width"], el["height"] = maxX-minX, maxY-minY
}

func arrowhead(v, def string) any {
	switch v {
	case "":
		if def == "" {
			return nil
		}
		return def
	case "none":
		return nil
	}
	return v
}

func (b *builder) buildArrow(i int) error {
	sk := &b.skeletons[i]
	id := b.ids[i]
	el := b.base(sk, id, "arrow", 0, 0, 0, 0)
	el["startArrowhead"] = arrowhead(sk.StartArrowhead, "")
	el["endArrowhead"] = arrowhead(sk.EndArrowhead, "arrow")
	el["elbowed"] = false
	el["startBinding"] = nil
	el["endBinding"] = nil

	var start, end Element
	if sk.Start != nil {
		start = b.target(sk.Start.ID)
	}
	if sk.End != nil {
		end = b.target(sk.End.ID)
	}
	if start != nil && end != nil {
		sx, sy := center(start)
		ex, ey := center(end)
		p0x, p0y := edgePoint(start, ex, ey)
		p1x, p1y := edgePoint(end, sx, sy)
		setLinearGeometry(el, 0, 0, [][2]float64{{p0x, p0y}, {p1x, p1y}})
	} else {
		setLinearGeometry(el, *sk.X, *sk.Y, sk.Points)
	}
	pts := points(el)
	ax, ay := el.num("x"), el.num("y")
	if start != nil {
		b.bind(el, "startBinding", start, ax+pts[0][0], ay+pts[0][1])
	}
	if end != nil {
		last := pts[len(pts)-1]
		b.bind(el, "endBinding", end, ax+last[0], ay+last[1])
	}
	b.built[id] = el
	if sk.Label != nil {
		b.addLabel(el, sk, false, false)
	}
	return nil
}

// bind attaches an arrow end to target, mirroring
// calculateFixedPointForNonElbowArrowBinding (packages/element/src/binding.ts):
// the fixed point is the endpoint's position relative to the target's box.
func (b *builder) bind(arrow Element, key string, target Element, px, py float64) {
	gap := bindingGap(target)
	fx := (px - target.num("x")) / math.Max(target.num("width"), gap)
	fy := (py - target.num("y")) / math.Max(target.num("height"), gap)
	arrow[key] = map[string]any{
		"elementId":  target.ID(),
		"fixedPoint": []any{fx, fy},
		"mode":       "orbit",
	}
	target.setBoundRefs(append(target.boundRefs(), boundRef{Type: "arrow", ID: arrow.ID()}))
	if _, isNew := b.built[target.ID()]; !isNew {
		if b.touched == nil {
			b.touched = map[string]Element{}
		}
		b.touched[target.ID()] = target
	}
}

func (b *builder) buildFrame(i int) error {
	sk := &b.skeletons[i]
	id := b.ids[i]
	el := b.base(sk, id, "frame", 0, 0, 0, 0)
	el["roundness"] = nil
	if sk.Name != "" {
		el["name"] = sk.Name
	} else {
		el["name"] = nil
	}
	x, y := sk.X, sk.Y
	width, height := sk.Width, sk.Height
	if x == nil || y == nil || width == nil || height == nil {
		// fit the children, like convertToExcalidrawElements (10px padding)
		minX, minY, maxX, maxY, ok := b.childBounds(id)
		if !ok {
			return inputErr("elements[%d]: a frame without x, y, width and height needs elements with frameId %q", i, id)
		}
		const padding = 10
		if x == nil {
			v := minX - padding
			x = &v
		}
		if y == nil {
			v := minY - padding
			y = &v
		}
		if width == nil {
			v := maxX + padding - *x
			width = &v
		}
		if height == nil {
			v := maxY + padding - *y
			height = &v
		}
	}
	el["x"], el["y"] = *x, *y
	el["width"], el["height"] = *width, *height
	b.built[id] = el
	return nil
}

// childBounds is the bounding box of the new elements placed in a frame.
func (b *builder) childBounds(frameID string) (minX, minY, maxX, maxY float64, ok bool) {
	minX, minY, maxX, maxY = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for j := range b.skeletons {
		if b.skeletons[j].FrameID != frameID {
			continue
		}
		el := b.built[b.ids[j]]
		x, y := el.num("x"), el.num("y")
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x+el.num("width")), math.Max(maxY, y+el.num("height"))
		ok = true
	}
	return minX, minY, maxX, maxY, ok
}
