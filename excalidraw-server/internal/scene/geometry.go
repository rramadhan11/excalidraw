package scene

import "math"

// baseBindingGap is BASE_BINDING_GAP (packages/element/src/binding.ts).
const baseBindingGap = 5

// bindingGap mirrors getBindingGap: how far outside the outline a bound
// arrow's end sits.
func bindingGap(el Element) float64 {
	return baseBindingGap + el.num("strokeWidth")/2
}

func center(el Element) (float64, float64) {
	return el.num("x") + el.num("width")/2, el.num("y") + el.num("height")/2
}

// edgePoint is where the line from el's centre towards (tx, ty) leaves el's
// outline, pushed out by the binding gap. Rotation is ignored: elements made
// here are never rotated, and for rotated existing ones the editor re-routes
// the arrow as soon as either end is touched.
func edgePoint(el Element, tx, ty float64) (float64, float64) {
	cx, cy := center(el)
	dx, dy := tx-cx, ty-cy
	dist := math.Hypot(dx, dy)
	if dist == 0 {
		return cx, cy
	}
	a, b := el.num("width")/2, el.num("height")/2
	var t float64 // fraction of (dx, dy) that reaches the outline
	switch el.Type() {
	case "ellipse":
		t = 1 / math.Sqrt((dx*dx)/(a*a)+(dy*dy)/(b*b))
	case "diamond":
		t = 1 / (math.Abs(dx)/a + math.Abs(dy)/b)
	default:
		t = math.Inf(1)
		if dx != 0 {
			t = math.Min(t, a/math.Abs(dx))
		}
		if dy != 0 {
			t = math.Min(t, b/math.Abs(dy))
		}
	}
	if math.IsInf(t, 0) || math.IsNaN(t) || a == 0 || b == 0 {
		return cx, cy
	}
	gap := bindingGap(el)
	scale := t + gap/dist
	return cx + dx*scale, cy + dy*scale
}
