package scene

import (
	"fmt"
	"math"
	"slices"
)

// Update changes some properties of one existing element. Omitted fields are
// left as they are.
type Update struct {
	ID     string   `json:"id" jsonschema:"id of the element to change"`
	X      *float64 `json:"x,omitempty" jsonschema:"new left edge; moving a shape also moves its label and re-routes attached arrows, moving a frame moves its contents"`
	Y      *float64 `json:"y,omitempty" jsonschema:"new top edge"`
	Width  *float64 `json:"width,omitempty" jsonschema:"new width (rectangle, ellipse, diamond, frame)"`
	Height *float64 `json:"height,omitempty" jsonschema:"new height (rectangle, ellipse, diamond, frame)"`
	Text   *string  `json:"text,omitempty" jsonschema:"new content of a text element, or new label of a shape/arrow (empty string removes the label)"`
	// FontSize applies to text elements and labels.
	FontSize        *float64 `json:"fontSize,omitempty" jsonschema:"new font size of a text element or of the label"`
	StrokeColor     string   `json:"strokeColor,omitempty" jsonschema:"hex color"`
	BackgroundColor string   `json:"backgroundColor,omitempty" jsonschema:"hex color or transparent"`
	FillStyle       string   `json:"fillStyle,omitempty" jsonschema:"solid, hachure, cross-hatch or zigzag"`
	StrokeStyle     string   `json:"strokeStyle,omitempty" jsonschema:"solid, dashed or dotted"`
	StrokeWidth     *float64 `json:"strokeWidth,omitempty" jsonschema:"1, 2 or 4"`
	Opacity         *int     `json:"opacity,omitempty" jsonschema:"0-100"`
	Name            *string  `json:"name,omitempty" jsonschema:"new frame title"`
}

func (u *Update) validate(path string) error {
	if u.ID == "" {
		return inputErr("%s.id is required", path)
	}
	for field, v := range map[string]*float64{"x": u.X, "y": u.Y} {
		if err := checkCoord(path, field, v); err != nil {
			return err
		}
	}
	for field, v := range map[string]*float64{"width": u.Width, "height": u.Height} {
		if err := checkSize(path, field, v); err != nil {
			return err
		}
	}
	if u.Text != nil {
		if err := checkText(path+".text", *u.Text); err != nil {
			return err
		}
	}
	if u.Name != nil {
		if err := checkText(path+".name", *u.Name); err != nil {
			return err
		}
	}
	if err := checkFontSize(path, u.FontSize); err != nil {
		return err
	}
	if err := checkColor(path, "strokeColor", u.StrokeColor); err != nil {
		return err
	}
	if err := checkColor(path, "backgroundColor", u.BackgroundColor); err != nil {
		return err
	}
	if err := checkEnum(path, "fillStyle", u.FillStyle, fillStyles); err != nil {
		return err
	}
	if err := checkEnum(path, "strokeStyle", u.StrokeStyle, strokeStyles); err != nil {
		return err
	}
	if u.StrokeWidth != nil && (math.IsNaN(*u.StrokeWidth) || *u.StrokeWidth < 0.5 || *u.StrokeWidth > 16) {
		return inputErr("%s.strokeWidth must be between 0.5 and 16", path)
	}
	if u.Opacity != nil && (*u.Opacity < 0 || *u.Opacity > 100) {
		return inputErr("%s.opacity must be 0-100", path)
	}
	return nil
}

// boundText returns a container's live label, if any.
func (s *Scene) boundText(container Element) (Element, bool) {
	for _, ref := range container.boundRefs() {
		if ref.Type != "text" {
			continue
		}
		if el, ok := s.live(ref.ID); ok && el.ContainerID() == container.ID() {
			return el, true
		}
	}
	return nil, false
}

// Apply performs the updates in order. Nothing is changed if any update is
// invalid.
func (s *Scene) Apply(updates []Update) error {
	if len(updates) == 0 {
		return inputErr("updates is empty")
	}
	if len(updates) > MaxElementsPerCall {
		return inputErr("at most %d updates per call", MaxElementsPerCall)
	}
	for i := range updates {
		path := fmt.Sprintf("updates[%d]", i)
		if err := updates[i].validate(path); err != nil {
			return err
		}
		if err := s.checkUpdate(path, &updates[i]); err != nil {
			return err
		}
	}
	touched := map[string]Element{}
	moved := map[string]bool{}
	for i := range updates {
		s.applyOne(&updates[i], touched, moved)
	}
	s.rerouteArrows(moved, touched)
	s.touchAll(touched)
	return nil
}

func (s *Scene) checkUpdate(path string, u *Update) error {
	el, ok := s.live(u.ID)
	if !ok {
		return inputErr("%s.id %q does not exist on this board", path, u.ID)
	}
	typ := el.Type()
	if el.ContainerID() != "" {
		return inputErr("%s.id %q is the label of %q; update that element's text instead", path, u.ID, el.ContainerID())
	}
	if (u.Width != nil || u.Height != nil) && !shapeTypes[typ] && typ != "frame" {
		return inputErr("%s: width and height can only be changed on rectangles, ellipses, diamonds and frames", path)
	}
	if u.Text != nil && !shapeTypes[typ] && typ != "arrow" && typ != "text" {
		return inputErr("%s: a %s has no text", path, typ)
	}
	if u.Text != nil && typ == "text" && *u.Text == "" {
		return inputErr("%s: use delete_elements to remove a text element", path)
	}
	if u.Name != nil && typ != "frame" {
		return inputErr("%s: name only applies to frames", path)
	}
	if typ == "arrow" && (u.X != nil || u.Y != nil) {
		if _, _, _, bound := el.binding("startBinding"); bound {
			return inputErr("%s: arrow %q is attached to elements; move those instead", path, u.ID)
		}
		if _, _, _, bound := el.binding("endBinding"); bound {
			return inputErr("%s: arrow %q is attached to elements; move those instead", path, u.ID)
		}
	}
	return nil
}

func (s *Scene) applyOne(u *Update, touched map[string]Element, moved map[string]bool) {
	el, _ := s.live(u.ID)
	typ := el.Type()
	touched[el.ID()] = el

	for key, v := range map[string]string{
		"strokeColor": u.StrokeColor, "backgroundColor": u.BackgroundColor,
		"fillStyle": u.FillStyle, "strokeStyle": u.StrokeStyle,
	} {
		if v == "" {
			continue
		}
		if key == "strokeColor" {
			// a label drawn in its container's color keeps following it
			if label, ok := s.boundText(el); ok && label.str("strokeColor") == el.str("strokeColor") {
				label["strokeColor"] = v
				touched[label.ID()] = label
			}
		}
		el[key] = v
	}
	if u.StrokeWidth != nil {
		el["strokeWidth"] = *u.StrokeWidth
	}
	if u.Opacity != nil {
		el["opacity"] = *u.Opacity
	}
	if u.Name != nil {
		if *u.Name == "" {
			el["name"] = nil
		} else {
			el["name"] = *u.Name
		}
	}

	dx, dy := 0.0, 0.0
	if u.X != nil {
		dx = *u.X - el.num("x")
	}
	if u.Y != nil {
		dy = *u.Y - el.num("y")
	}
	resized := false
	if u.Width != nil {
		el["width"] = *u.Width
		resized = true
	}
	if u.Height != nil {
		el["height"] = *u.Height
		resized = true
	}

	switch {
	case typ == "text":
		if u.Text != nil {
			el["originalText"] = normalizeText(*u.Text)
			el["text"] = el["originalText"]
		}
		if u.FontSize != nil {
			el["fontSize"] = *u.FontSize
		}
		if u.Text != nil || u.FontSize != nil {
			s.remeasureText(el)
		}
		s.translate(el, dx, dy)
		if dx != 0 || dy != 0 {
			moved[el.ID()] = true
		}
	case typ == "frame":
		if dx != 0 || dy != 0 {
			for _, child := range s.Elements {
				if child.Deleted() || child.str("frameId") != el.ID() {
					continue
				}
				s.translate(child, dx, dy)
				touched[child.ID()] = child
				moved[child.ID()] = true
			}
			s.translate(el, dx, dy)
		}
	default: // shapes and linear elements
		s.translate(el, dx, dy)
		if dx != 0 || dy != 0 || resized {
			moved[el.ID()] = true
		}
	}

	if typ != "text" && (u.Text != nil || u.FontSize != nil) {
		s.setLabel(el, u, touched)
	} else if label, ok := s.boundText(el); ok && (dx != 0 || dy != 0 || resized) {
		layoutLabel(el, label, false)
		touched[label.ID()] = label
	}
}

func (s *Scene) translate(el Element, dx, dy float64) {
	if dx == 0 && dy == 0 {
		return
	}
	el["x"] = el.num("x") + dx
	el["y"] = el.num("y") + dy
	if label, ok := s.boundText(el); ok && el.Type() != "arrow" {
		label["x"] = label.num("x") + dx
		label["y"] = label.num("y") + dy
	}
}

func (s *Scene) remeasureText(el Element) {
	fontFamily := int(el.num("fontFamily"))
	if fontFamily == 0 {
		fontFamily = defaultFontFamily
	}
	m := measure(el.str("text"), el.num("fontSize"), fontFamily)
	el["width"], el["height"] = m.width, m.height
}

// setLabel replaces, creates or removes a shape's or arrow's label.
func (s *Scene) setLabel(container Element, u *Update, touched map[string]Element) {
	label, has := s.boundText(container)
	if u.Text != nil && *u.Text == "" {
		if has {
			label["isDeleted"] = true
			touched[label.ID()] = label
			s.unref(container, label.ID())
		}
		return
	}
	if !has {
		if u.Text == nil {
			return // only a font size for a label that doesn't exist
		}
		b := &builder{scene: s, built: map[string]Element{}}
		sk := &Skeleton{Label: &Label{Text: *u.Text, FontSize: u.FontSize}}
		b.addLabel(container, sk, false, false)
		label = b.labels[container.ID()]
		s.append(label)
		touched[label.ID()] = label
		return
	}
	if u.Text != nil {
		label["originalText"] = normalizeText(*u.Text)
	}
	if u.FontSize != nil {
		label["fontSize"] = *u.FontSize
	}
	layoutLabel(container, label, false)
	touched[label.ID()] = label
}

func (s *Scene) unref(el Element, id string) {
	refs := el.boundRefs()
	kept := slices.DeleteFunc(refs, func(r boundRef) bool { return r.ID == id })
	el.setBoundRefs(kept)
}

// rerouteArrows moves the ends of arrows bound to moved or resized elements
// back onto their fixed points, as the editor does when you drag a shape.
func (s *Scene) rerouteArrows(moved map[string]bool, touched map[string]Element) {
	if len(moved) == 0 {
		return
	}
	for _, arrow := range s.Elements {
		if arrow.Deleted() || arrow.Type() != "arrow" {
			continue
		}
		startID, sfx, sfy, hasStart := arrow.binding("startBinding")
		endID, efx, efy, hasEnd := arrow.binding("endBinding")
		if !(hasStart && moved[startID]) && !(hasEnd && moved[endID]) {
			continue
		}
		pts := points(arrow)
		if len(pts) < 2 {
			continue
		}
		ax, ay := arrow.num("x"), arrow.num("y")
		abs := make([][2]float64, len(pts))
		for i, p := range pts {
			abs[i] = [2]float64{ax + p[0], ay + p[1]}
		}
		if target, ok := s.live(startID); hasStart && ok {
			abs[0] = fixedPointPosition(target, sfx, sfy)
		}
		if target, ok := s.live(endID); hasEnd && ok {
			abs[len(abs)-1] = fixedPointPosition(target, efx, efy)
		}
		setLinearGeometry(arrow, 0, 0, abs)
		touched[arrow.ID()] = arrow
		if label, ok := s.boundText(arrow); ok {
			layoutLabel(arrow, label, false)
			touched[label.ID()] = label
		}
	}
}

func fixedPointPosition(el Element, fx, fy float64) [2]float64 {
	gap := bindingGap(el)
	return [2]float64{
		el.num("x") + fx*math.Max(el.num("width"), gap),
		el.num("y") + fy*math.Max(el.num("height"), gap),
	}
}

// Delete marks elements deleted (they stay in the scene with isDeleted, as
// the editor does) together with what can't exist without them: labels, and
// a frame's contents. Arrows attached to a deleted element are detached, not
// deleted. It returns every id that was deleted.
func (s *Scene) Delete(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, inputErr("ids is empty")
	}
	if len(ids) > MaxElementsPerCall {
		return nil, inputErr("at most %d ids per call", MaxElementsPerCall)
	}
	var missing []string
	for _, id := range ids {
		if _, ok := s.live(id); !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, inputErr("not on this board (or already deleted): %v", missing)
	}

	doomed := map[string]bool{}
	var mark func(id string)
	mark = func(id string) {
		el, ok := s.live(id)
		if !ok || doomed[id] {
			return
		}
		doomed[id] = true
		if label, ok := s.boundText(el); ok {
			mark(label.ID())
		}
		if el.Type() == "frame" {
			for _, child := range s.Elements {
				if !child.Deleted() && child.str("frameId") == id {
					mark(child.ID())
				}
			}
		}
	}
	for _, id := range ids {
		mark(id)
	}

	touched := map[string]Element{}
	var deleted []string
	for _, el := range s.Elements {
		id := el.ID()
		if doomed[id] {
			el["isDeleted"] = true
			touched[id] = el
			deleted = append(deleted, id)
		} else if el.Deleted() {
			continue
		} else {
			// detach survivors from what was deleted
			for _, key := range []string{"startBinding", "endBinding"} {
				if target, _, _, ok := el.binding(key); ok && doomed[target] {
					el[key] = nil
					touched[id] = el
				}
			}
		}
		// no element lists a deleted one as bound, as restore's
		// repairContainerElement would enforce
		refs := el.boundRefs()
		kept := slices.DeleteFunc(slices.Clone(refs), func(r boundRef) bool { return doomed[r.ID] })
		if len(kept) != len(refs) {
			el.setBoundRefs(kept)
			touched[id] = el
		}
	}
	s.touchAll(touched)
	return deleted, nil
}
