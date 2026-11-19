package scene

import "math"

// Summary is a compact, model-friendly view of a scene.
type Summary struct {
	// ElementCount counts live elements, labels excluded.
	ElementCount int `json:"elementCount"`
	// Bounds of all live elements; nil for an empty board.
	Bounds    *Bounds          `json:"bounds"`
	Elements  []SummaryElement `json:"elements"`
	Truncated bool             `json:"truncated,omitempty"`
}

type Bounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type SummaryElement struct {
	ID              string  `json:"id"`
	Type            string  `json:"type"`
	X               float64 `json:"x"`
	Y               float64 `json:"y"`
	Width           float64 `json:"width"`
	Height          float64 `json:"height"`
	Text            string  `json:"text,omitempty"`
	Label           string  `json:"label,omitempty"`
	Name            string  `json:"name,omitempty"`
	From            string  `json:"from,omitempty"`
	To              string  `json:"to,omitempty"`
	StrokeColor     string  `json:"strokeColor,omitempty"`
	BackgroundColor string  `json:"backgroundColor,omitempty"`
	FrameID         string  `json:"frameId,omitempty"`
	FileID          string  `json:"fileId,omitempty"`
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// Summarize lists live elements in z-order (labels folded into their
// containers), up to limit. Default colors are omitted to keep it short.
func (s *Scene) Summarize(limit int) Summary {
	sum := Summary{Elements: []SummaryElement{}}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, el := range s.Elements {
		if el.Deleted() || el.ContainerID() != "" {
			continue
		}
		x, y, w, h := el.num("x"), el.num("y"), el.num("width"), el.num("height")
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x+w), math.Max(maxY, y+h)
		sum.ElementCount++
		if len(sum.Elements) >= limit {
			sum.Truncated = true
			continue
		}
		item := SummaryElement{
			ID: el.ID(), Type: el.Type(),
			X: round1(x), Y: round1(y), Width: round1(w), Height: round1(h),
			FrameID: el.str("frameId"),
			FileID:  el.str("fileId"),
		}
		switch el.Type() {
		case "text":
			item.Text = el.str("originalText")
			if item.Text == "" {
				item.Text = el.str("text")
			}
		case "frame", "magicframe":
			item.Name = el.str("name")
		}
		if label, ok := s.boundText(el); ok {
			item.Label = label.str("originalText")
			if item.Label == "" {
				item.Label = label.str("text")
			}
		}
		if id, _, _, ok := el.binding("startBinding"); ok {
			item.From = id
		}
		if id, _, _, ok := el.binding("endBinding"); ok {
			item.To = id
		}
		if c := el.str("strokeColor"); c != "#1e1e1e" {
			item.StrokeColor = c
		}
		if c := el.str("backgroundColor"); c != "transparent" {
			item.BackgroundColor = c
		}
		sum.Elements = append(sum.Elements, item)
	}
	if sum.ElementCount > 0 {
		sum.Bounds = &Bounds{X: round1(minX), Y: round1(minY), Width: round1(maxX - minX), Height: round1(maxY - minY)}
	}
	return sum
}

// LiveElements returns the non-deleted elements as stored, up to limit.
func (s *Scene) LiveElements(limit int) ([]Element, bool) {
	out := []Element{}
	for _, el := range s.Elements {
		if el.Deleted() {
			continue
		}
		if len(out) >= limit {
			return out, true
		}
		out = append(out, el)
	}
	return out, false
}
