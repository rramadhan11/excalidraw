package scene

import (
	"math"
	"strings"
	"unicode"
)

const (
	defaultFontSize   = 20 // DEFAULT_FONT_SIZE, packages/common/src/constants.ts
	defaultFontFamily = 5  // FONT_FAMILY.Excalifont
	codeFontFamily    = 8  // FONT_FAMILY["Comic Shanns"], monospaced
	boundTextPadding  = 5  // BOUND_TEXT_PADDING
	// ARROW_LABEL_WIDTH_FRACTION and ARROW_LABEL_FONT_SIZE_TO_MIN_WIDTH_RATIO
	arrowLabelWidthFraction = 0.7
	arrowLabelMinWidthPerPx = 11
	defaultShapeSize        = 100 // DEFAULT_DIMENSION, packages/element/src/transform.ts
	// FONT_METADATA[...].metrics.lineHeight (packages/common/src/font-metadata.ts)
	// is 1.25 for all three families offered here
	textLineHeight = 1.25
)

// charWidth estimates a glyph's advance in em. The server has no font
// renderer; the editor re-measures text the next time it is edited, and labels
// are centred on their container whatever their measured width, so an
// estimate is enough for a correct-looking first render.
func charWidth(r rune, fontFamily int) float64 {
	if fontFamily == codeFontFamily {
		if isWide(r) {
			return 1.2
		}
		return 0.6
	}
	switch {
	case isWide(r):
		return 1.0
	case r == ' ':
		return 0.3
	case strings.ContainsRune("iljI.,:;'|!`()[]{}", r):
		return 0.32
	case strings.ContainsRune("mwMW@%", r):
		return 0.85
	case unicode.IsUpper(r):
		return 0.66
	case unicode.IsDigit(r):
		return 0.56
	default:
		return 0.53
	}
}

// isWide reports CJK, emoji and other glyphs drawn about a full em wide.
func isWide(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) ||
		(r >= 0x1F300 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF)
}

func lineWidth(line string, fontSize float64, fontFamily int) float64 {
	w := 0.0
	for _, r := range line {
		w += charWidth(r, fontFamily)
	}
	return w * fontSize
}

// normalizeText mirrors normalizeText() in the editor: tabs become spaces and
// line endings become \n.
func normalizeText(text string) string {
	text = strings.ReplaceAll(text, "\t", "        ")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

type textMetrics struct {
	width, height float64
}

func measure(text string, fontSize float64, fontFamily int) textMetrics {
	lines := strings.Split(text, "\n")
	width := 0.0
	for _, line := range lines {
		width = math.Max(width, lineWidth(line, fontSize, fontFamily))
	}
	return textMetrics{
		width:  math.Ceil(width),
		height: float64(len(lines)) * fontSize * textLineHeight,
	}
}

// wrap breaks text into lines no wider than maxWidth, at spaces where
// possible, like wrapText() does for bound text. Explicit newlines are kept.
func wrap(text string, maxWidth, fontSize float64, fontFamily int) string {
	if maxWidth <= 0 {
		return text
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		out = append(out, wrapLine(para, maxWidth, fontSize, fontFamily)...)
	}
	return strings.Join(out, "\n")
}

func wrapLine(line string, maxWidth, fontSize float64, fontFamily int) []string {
	if lineWidth(line, fontSize, fontFamily) <= maxWidth {
		return []string{line}
	}
	var lines []string
	current := ""
	for _, word := range strings.Fields(line) {
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if lineWidth(candidate, fontSize, fontFamily) <= maxWidth {
			current = candidate
			continue
		}
		if current != "" {
			lines = append(lines, current)
			current = ""
		}
		// a single word wider than the line is broken by character
		for lineWidth(word, fontSize, fontFamily) > maxWidth {
			cut := 0
			w := 0.0
			for i, r := range word {
				w += charWidth(r, fontFamily) * fontSize
				if w > maxWidth && cut > 0 {
					break
				}
				cut = i + len(string(r))
			}
			lines = append(lines, word[:cut])
			word = word[cut:]
		}
		current = word
	}
	if current != "" || len(lines) == 0 {
		lines = append(lines, current)
	}
	return lines
}

// boundTextMaxWidth mirrors getBoundTextMaxWidth
// (packages/element/src/textElement.ts).
func boundTextMaxWidth(containerType string, width, fontSize float64) float64 {
	switch containerType {
	case "arrow":
		return math.Max(arrowLabelWidthFraction*width, fontSize*arrowLabelMinWidthPerPx)
	case "ellipse":
		return math.Round(width/2*math.Sqrt2) - boundTextPadding*2
	case "diamond":
		return math.Round(width/2) - boundTextPadding*2
	}
	return width - boundTextPadding*2
}

// boundTextMaxHeight mirrors getBoundTextMaxHeight for shape containers.
func boundTextMaxHeight(containerType string, height float64) float64 {
	switch containerType {
	case "ellipse":
		return math.Round(height/2*math.Sqrt2) - boundTextPadding*2
	case "diamond":
		return math.Round(height/2) - boundTextPadding*2
	}
	return height - boundTextPadding*2
}

// containerDimensionForText mirrors computeContainerDimensionForBoundText:
// the container size needed to fit text of the given size.
func containerDimensionForText(dimension float64, containerType string) float64 {
	dimension = math.Ceil(dimension)
	padding := float64(boundTextPadding * 2)
	switch containerType {
	case "ellipse":
		return math.Round((dimension + padding) / math.Sqrt2 * 2)
	case "diamond":
		return 2 * (dimension + padding)
	}
	return dimension + padding
}

// containerTextOrigin mirrors getContainerCoords: the top-left of the area
// bound text is laid out in.
func containerTextOrigin(containerType string, x, y, width, height float64) (float64, float64) {
	offsetX, offsetY := float64(boundTextPadding), float64(boundTextPadding)
	switch containerType {
	case "ellipse":
		offsetX += width / 2 * (1 - math.Sqrt2/2)
		offsetY += height / 2 * (1 - math.Sqrt2/2)
	case "diamond":
		offsetX += width / 4
		offsetY += height / 4
	}
	return x + offsetX, y + offsetY
}
