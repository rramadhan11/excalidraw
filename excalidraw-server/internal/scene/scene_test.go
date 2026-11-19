package scene

import (
	"bytes"
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden fixture used by excalidraw-app/tests/mcpScene.test.ts")

func ptr[T any](v T) *T { return &v }

func emptyScene(t *testing.T) *Scene {
	t.Helper()
	s, err := Parse(json.RawMessage(`{"elements":[],"appState":{"viewBackgroundColor":"#ffffff"}}`))
	if err != nil {
		t.Fatal(err)
	}
	s.WithDeterminism(1, time.UnixMilli(1_700_000_000_000))
	return s
}

func mustAdd(t *testing.T, s *Scene, sks ...Skeleton) []string {
	t.Helper()
	ids, err := s.Add(sks)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return ids
}

func get(t *testing.T, s *Scene, id string) Element {
	t.Helper()
	el, ok := s.byID[id]
	if !ok {
		t.Fatalf("element %q not found", id)
	}
	return el
}

func refIDs(el Element) []string {
	var ids []string
	for _, r := range el.boundRefs() {
		ids = append(ids, r.Type+":"+r.ID)
	}
	return ids
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestAddLabelledShapesAndBoundArrow(t *testing.T) {
	s := emptyScene(t)
	mustAdd(t, s,
		Skeleton{ID: "a", Type: "rectangle", X: ptr(0.0), Y: ptr(0.0), Width: ptr(200.0), Height: ptr(80.0), Label: &Label{Text: "API"}},
		Skeleton{ID: "b", Type: "ellipse", X: ptr(400.0), Y: ptr(0.0), Width: ptr(160.0), Height: ptr(80.0), Label: &Label{Text: "DB"}},
		Skeleton{ID: "ab", Type: "arrow", Start: &Ref{ID: "a"}, End: &Ref{ID: "b"}, Label: &Label{Text: "SQL"}},
	)
	// a, its label, b, its label, the arrow, its label
	if len(s.Elements) != 6 {
		t.Fatalf("got %d elements, want 6", len(s.Elements))
	}
	a, b, ab := get(t, s, "a"), get(t, s, "b"), get(t, s, "ab")

	for _, container := range []Element{a, b, ab} {
		label, ok := s.boundText(container)
		if !ok {
			t.Fatalf("%s has no label", container.ID())
		}
		if label.ContainerID() != container.ID() || label.str("textAlign") != "center" || label.str("verticalAlign") != "middle" {
			t.Errorf("label of %s: containerId=%q align=%q/%q", container.ID(), label.ContainerID(), label.str("textAlign"), label.str("verticalAlign"))
		}
	}
	if !reflect.DeepEqual(refIDs(a)[1:], []string{"arrow:ab"}) || !reflect.DeepEqual(refIDs(b)[1:], []string{"arrow:ab"}) {
		t.Errorf("boundElements a=%v b=%v", refIDs(a), refIDs(b))
	}

	startID, sfx, sfy, ok := ab.binding("startBinding")
	if !ok || startID != "a" {
		t.Fatalf("startBinding = %v", ab["startBinding"])
	}
	endID, efx, efy, ok := ab.binding("endBinding")
	if !ok || endID != "b" {
		t.Fatalf("endBinding = %v", ab["endBinding"])
	}
	gap := bindingGap(a) // 5 + strokeWidth/2 = 6
	// along the horizontal centre line: just outside a's right edge and b's left edge
	if !near(ab.num("x"), 200+gap) || !near(ab.num("y"), 40) {
		t.Errorf("arrow starts at (%v, %v), want (%v, 40)", ab.num("x"), ab.num("y"), 200+gap)
	}
	pts := points(ab)
	if !near(ab.num("x")+pts[1][0], 400-gap) || !near(pts[1][1], 0) {
		t.Errorf("arrow ends at x=%v, want %v", ab.num("x")+pts[1][0], 400-gap)
	}
	if !near(sfx, (200+gap)/200) || !near(sfy, 0.5) || !near(efx, -gap/160) || !near(efy, 0.5) {
		t.Errorf("fixed points start=(%v,%v) end=(%v,%v)", sfx, sfy, efx, efy)
	}
	if ab["endArrowhead"] != "arrow" || ab["startArrowhead"] != nil {
		t.Errorf("arrowheads %v / %v", ab["startArrowhead"], ab["endArrowhead"])
	}
}

func TestLabelFitsAndWraps(t *testing.T) {
	s := emptyScene(t)
	mustAdd(t, s,
		Skeleton{ID: "fit", Type: "rectangle", X: ptr(0.0), Y: ptr(0.0), Label: &Label{Text: "Hello"}},
		Skeleton{ID: "narrow", Type: "rectangle", X: ptr(0.0), Y: ptr(200.0), Width: ptr(100.0), Height: ptr(40.0),
			Label: &Label{Text: "a fairly long label that cannot fit on one line"}},
	)
	fit := get(t, s, "fit")
	label, _ := s.boundText(fit)
	if fit.num("width") < label.num("width")+2*boundTextPadding || fit.num("height") < label.num("height")+2*boundTextPadding {
		t.Errorf("container %vx%v does not fit label %vx%v", fit.num("width"), fit.num("height"), label.num("width"), label.num("height"))
	}

	narrow := get(t, s, "narrow")
	label, _ = s.boundText(narrow)
	if !strings.Contains(label.str("text"), "\n") {
		t.Errorf("long label not wrapped: %q", label.str("text"))
	}
	if label.str("originalText") != "a fairly long label that cannot fit on one line" {
		t.Errorf("originalText = %q", label.str("originalText"))
	}
	if narrow.num("height") <= 40 {
		t.Errorf("container did not grow for wrapped label: height %v", narrow.num("height"))
	}
	// centred inside the container
	cx := narrow.num("x") + narrow.num("width")/2
	if !near(label.num("x")+label.num("width")/2, cx) {
		t.Errorf("label centre %v, container centre %v", label.num("x")+label.num("width")/2, cx)
	}
}

func TestAddRejectsInvalidInputAtomically(t *testing.T) {
	base := func() *Scene {
		s := emptyScene(t)
		mustAdd(t, s,
			Skeleton{ID: "r", Type: "rectangle", X: ptr(0.0), Y: ptr(0.0)},
			Skeleton{ID: "l", Type: "line", X: ptr(0.0), Y: ptr(0.0), Points: [][2]float64{{0, 0}, {10, 10}}},
		)
		return s
	}
	cases := map[string][]Skeleton{
		"unknown type":      {{Type: "image", X: ptr(0.0), Y: ptr(0.0)}},
		"missing position":  {{Type: "rectangle"}},
		"bad color":         {{Type: "rectangle", X: ptr(0.0), Y: ptr(0.0), StrokeColor: "red"}},
		"non-finite":        {{Type: "rectangle", X: ptr(math.Inf(1)), Y: ptr(0.0)}},
		"negative size":     {{Type: "rectangle", X: ptr(0.0), Y: ptr(0.0), Width: ptr(-5.0)}},
		"missing target":    {{Type: "arrow", Start: &Ref{ID: "r"}, End: &Ref{ID: "nope"}}},
		"unbindable target": {{Type: "arrow", Start: &Ref{ID: "r"}, End: &Ref{ID: "l"}}},
		"self arrow":        {{Type: "arrow", Start: &Ref{ID: "r"}, End: &Ref{ID: "r"}}},
		"existing id":       {{ID: "r", Type: "rectangle", X: ptr(0.0), Y: ptr(0.0)}},
		"duplicate id":      {{ID: "x", Type: "rectangle", X: ptr(0.0), Y: ptr(0.0)}, {ID: "x", Type: "ellipse", X: ptr(0.0), Y: ptr(0.0)}},
		"bad id":            {{ID: "a b", Type: "rectangle", X: ptr(0.0), Y: ptr(0.0)}},
		"text on shape":     {{Type: "rectangle", X: ptr(0.0), Y: ptr(0.0), Text: "hi"}},
		"frameId not frame": {{Type: "rectangle", X: ptr(0.0), Y: ptr(0.0), FrameID: "r"}},
		"too few points":    {{Type: "line", X: ptr(0.0), Y: ptr(0.0), Points: [][2]float64{{0, 0}}}},
		"empty frame":       {{Type: "frame", Name: "F"}},
		"later invalid": {
			{ID: "ok", Type: "rectangle", X: ptr(0.0), Y: ptr(0.0)},
			{Type: "diamond", X: ptr(0.0)},
		},
	}
	for name, sks := range cases {
		t.Run(name, func(t *testing.T) {
			s := base()
			before, _ := s.Marshal()
			_, err := s.Add(sks)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !IsInputError(err) {
				t.Errorf("error %v is not an InputError", err)
			}
			after, _ := s.Marshal()
			if !bytes.Equal(before, after) {
				t.Error("scene changed despite the error")
			}
		})
	}
	if _, err := emptyScene(t).Add(make([]Skeleton, MaxElementsPerCall+1)); err == nil {
		t.Error("expected an error above MaxElementsPerCall")
	}
}

const existingScene = `{"elements":[
 {"id":"old","type":"rectangle","x":10,"y":20,"width":100,"height":50,"angle":0,
  "strokeColor":"#1e1e1e","backgroundColor":"transparent","fillStyle":"solid","strokeWidth":2,
  "strokeStyle":"solid","roughness":1,"opacity":100,"groupIds":[],"frameId":null,"index":"a0",
  "roundness":{"type":3},"seed":42,"version":7,"versionNonce":99,"isDeleted":false,
  "boundElements":null,"updated":1699999999999,"link":null,"locked":false,
  "customData":{"keep":true},"futureField":[1,2,3]}
],"appState":{"gridSize":20}}`

func TestRoundTripKeepsUnknownFieldsAndNumbers(t *testing.T) {
	s, err := Parse(json.RawMessage(existingScene))
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	_ = json.Unmarshal([]byte(existingScene), &want)
	_ = json.Unmarshal(out, &got)
	if !reflect.DeepEqual(want, got) {
		t.Errorf("round trip changed the scene:\n%s", out)
	}
}

func TestBindingToExistingElementBumpsItsVersion(t *testing.T) {
	s, _ := Parse(json.RawMessage(existingScene))
	s.WithDeterminism(2, time.UnixMilli(1_700_000_000_000))
	mustAdd(t, s,
		Skeleton{ID: "new", Type: "diamond", X: ptr(300.0), Y: ptr(0.0)},
		Skeleton{ID: "arr", Type: "arrow", Start: &Ref{ID: "old"}, End: &Ref{ID: "new"}},
	)
	old := get(t, s, "old")
	if !reflect.DeepEqual(refIDs(old), []string{"arrow:arr"}) {
		t.Errorf("old.boundElements = %v", refIDs(old))
	}
	if old.num("version") != 8 {
		t.Errorf("old.version = %v, want 8", old.num("version"))
	}
	if old["customData"] == nil || old["futureField"] == nil {
		t.Error("unknown fields of the touched element were lost")
	}
}

func TestApplyMovesLabelAndReroutesArrows(t *testing.T) {
	s := emptyScene(t)
	mustAdd(t, s,
		Skeleton{ID: "a", Type: "rectangle", X: ptr(0.0), Y: ptr(0.0), Width: ptr(100.0), Height: ptr(100.0), Label: &Label{Text: "A"}},
		Skeleton{ID: "b", Type: "rectangle", X: ptr(300.0), Y: ptr(0.0), Width: ptr(100.0), Height: ptr(100.0)},
		Skeleton{ID: "ab", Type: "arrow", Start: &Ref{ID: "a"}, End: &Ref{ID: "b"}},
	)
	a := get(t, s, "a")
	label, _ := s.boundText(a)
	labelX, abVersion := label.num("x"), get(t, s, "ab").num("version")

	if err := s.Apply([]Update{{ID: "b", Y: ptr(200.0)}, {ID: "a", X: ptr(-50.0)}}); err != nil {
		t.Fatal(err)
	}
	if !near(label.num("x"), labelX-50) {
		t.Errorf("label x = %v, want %v", label.num("x"), labelX-50)
	}
	ab := get(t, s, "ab")
	if ab.num("version") != abVersion+1 {
		t.Errorf("arrow version %v, want %v", ab.num("version"), abVersion+1)
	}
	pts := points(ab)
	end := [2]float64{ab.num("x") + pts[len(pts)-1][0], ab.num("y") + pts[len(pts)-1][1]}
	b := get(t, s, "b")
	_, efx, efy, _ := ab.binding("endBinding")
	if want := fixedPointPosition(b, efx, efy); !near(end[0], want[0]) || !near(end[1], want[1]) {
		t.Errorf("arrow end %v, want %v (on moved b)", end, want)
	}
	if !near(ab.num("x"), fixedPointPosition(a, 0, 0)[0]+(100+bindingGap(a))) {
		t.Errorf("arrow start x = %v did not follow a", ab.num("x"))
	}
}

func TestApplyLabels(t *testing.T) {
	s := emptyScene(t)
	mustAdd(t, s,
		Skeleton{ID: "r", Type: "rectangle", X: ptr(0.0), Y: ptr(0.0)},
		Skeleton{ID: "t", Type: "text", X: ptr(0.0), Y: ptr(200.0), Text: "hi"},
	)
	if err := s.Apply([]Update{{ID: "r", Text: ptr("new label")}, {ID: "t", Text: ptr("hello there")}}); err != nil {
		t.Fatal(err)
	}
	r := get(t, s, "r")
	label, ok := s.boundText(r)
	if !ok || label.str("originalText") != "new label" {
		t.Fatalf("label not created: %v", refIDs(r))
	}
	if txt := get(t, s, "t"); txt.str("text") != "hello there" || txt.num("width") <= 0 {
		t.Errorf("text element = %q width %v", txt.str("text"), txt.num("width"))
	}

	if err := s.Apply([]Update{{ID: "r", Text: ptr("")}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.boundText(r); ok || !label.Deleted() {
		t.Error("label not removed")
	}

	for name, u := range map[string]Update{
		"missing":      {ID: "nope", X: ptr(1.0)},
		"label direct": {ID: label.ID(), Text: ptr("x")},
		"resize text":  {ID: "t", Width: ptr(10.0)},
	} {
		if err := s.Apply([]Update{u}); err == nil || !IsInputError(err) {
			t.Errorf("%s: err = %v, want an InputError", name, err)
		}
	}
}

func TestDeleteCascades(t *testing.T) {
	s := emptyScene(t)
	mustAdd(t, s,
		Skeleton{ID: "f", Type: "frame", X: ptr(-20.0), Y: ptr(-20.0), Width: ptr(600.0), Height: ptr(300.0), Name: "Area"},
		Skeleton{ID: "a", Type: "rectangle", X: ptr(0.0), Y: ptr(0.0), Label: &Label{Text: "A"}, FrameID: "f"},
		Skeleton{ID: "b", Type: "rectangle", X: ptr(800.0), Y: ptr(0.0)},
		Skeleton{ID: "ab", Type: "arrow", Start: &Ref{ID: "a"}, End: &Ref{ID: "b"}},
	)
	label, _ := s.boundText(get(t, s, "a"))
	deleted, err := s.Delete([]string{"f"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"f": true, "a": true, label.ID(): true}
	if len(deleted) != len(want) {
		t.Errorf("deleted %v, want frame, its child and the child's label", deleted)
	}
	for _, id := range deleted {
		if !want[id] {
			t.Errorf("unexpectedly deleted %q", id)
		}
	}
	ab := get(t, s, "ab")
	if ab.Deleted() || ab["startBinding"] != nil {
		t.Errorf("arrow should survive detached: deleted=%v start=%v", ab.Deleted(), ab["startBinding"])
	}
	if _, err := s.Delete([]string{"a"}); err == nil {
		t.Error("deleting an already deleted element should fail")
	}
	if _, err := s.Delete([]string{"ab"}); err != nil {
		t.Fatal(err)
	}
	if refs := refIDs(get(t, s, "b")); len(refs) != 0 {
		t.Errorf("b still references %v", refs)
	}
}

func TestSummarize(t *testing.T) {
	s := emptyScene(t)
	mustAdd(t, s,
		Skeleton{ID: "a", Type: "rectangle", X: ptr(0.0), Y: ptr(0.0), Label: &Label{Text: "A"}, BackgroundColor: "#a5d8ff"},
		Skeleton{ID: "b", Type: "ellipse", X: ptr(300.0), Y: ptr(0.0)},
		Skeleton{ID: "ab", Type: "arrow", Start: &Ref{ID: "a"}, End: &Ref{ID: "b"}},
	)
	sum := s.Summarize(2)
	if sum.ElementCount != 3 || !sum.Truncated || len(sum.Elements) != 2 {
		t.Fatalf("count=%d truncated=%v listed=%d", sum.ElementCount, sum.Truncated, len(sum.Elements))
	}
	if first := sum.Elements[0]; first.Label != "A" || first.BackgroundColor != "#a5d8ff" || first.StrokeColor != "" {
		t.Errorf("first = %+v", first)
	}
	full := s.Summarize(100)
	if arrow := full.Elements[2]; arrow.From != "a" || arrow.To != "b" {
		t.Errorf("arrow = %+v", arrow)
	}
	if full.Bounds == nil || full.Bounds.X != 0 {
		t.Errorf("bounds = %+v", full.Bounds)
	}
}

func TestWrapBreaksLongWords(t *testing.T) {
	got := wrap("supercalifragilisticexpialidocious", 100, 20, defaultFontFamily)
	for _, line := range strings.Split(got, "\n") {
		if lineWidth(line, 20, defaultFontFamily) > 100 {
			t.Errorf("line %q is wider than 100", line)
		}
	}
	if strings.ReplaceAll(got, "\n", "") != "supercalifragilisticexpialidocious" {
		t.Errorf("characters lost: %q", got)
	}
}

// goldenScene exercises every element type and edit operation; its output is
// loaded through the editor's restoreElements by excalidraw-app.
func goldenScene(t *testing.T) *Scene {
	s := emptyScene(t)
	mustAdd(t, s,
		Skeleton{ID: "frame", Type: "frame", Name: "System"},
		Skeleton{ID: "api", Type: "rectangle", X: ptr(0.0), Y: ptr(0.0), Label: &Label{Text: "API gateway"}, BackgroundColor: "#a5d8ff", FrameID: "frame"},
		Skeleton{ID: "db", Type: "ellipse", X: ptr(320.0), Y: ptr(-20.0), Width: ptr(160.0), Height: ptr(100.0), Label: &Label{Text: "Postgres"}, FrameID: "frame"},
		Skeleton{ID: "auth", Type: "diamond", X: ptr(0.0), Y: ptr(200.0), Width: ptr(140.0), Height: ptr(100.0), Label: &Label{Text: "Auth?"}},
		Skeleton{ID: "title", Type: "text", X: ptr(200.0), Y: ptr(-120.0), Text: "Architecture\nv1", TextAlign: "center", FontSize: ptr(28.0)},
		Skeleton{ID: "sep", Type: "line", X: ptr(-40.0), Y: ptr(-60.0), Points: [][2]float64{{0, 0}, {560, 0}}, StrokeStyle: "dashed"},
		Skeleton{ID: "q", Type: "arrow", Start: &Ref{ID: "api"}, End: &Ref{ID: "db"}, Label: &Label{Text: "SQL"}},
		Skeleton{ID: "check", Type: "arrow", Start: &Ref{ID: "api"}, End: &Ref{ID: "auth"}, StrokeStyle: "dotted", StartArrowhead: "circle", EndArrowhead: "cardinality_zero_or_many"},
		Skeleton{ID: "loose", Type: "arrow", X: ptr(500.0), Y: ptr(250.0), Points: [][2]float64{{0, 0}, {-60, -40}, {-200, -40}}, End: &Ref{ID: "auth"}},
		Skeleton{ID: "note", Type: "rectangle", X: ptr(600.0), Y: ptr(200.0), Roundness: "sharp", Label: &Label{Text: "to be removed"}},
	)
	if err := s.Apply([]Update{
		{ID: "db", Y: ptr(40.0), Text: ptr("Postgres 17")},
		{ID: "auth", Width: ptr(180.0), StrokeColor: "#e03131"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete([]string{"note"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGoldenFixture(t *testing.T) {
	out, err := goldenScene(t).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	path := filepath.Join("..", "..", "..", "excalidraw-app", "tests", "fixtures", "mcp-scene.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/scene -update)", err)
	}
	if !bytes.Equal(want, pretty.Bytes()) {
		t.Errorf("%s is stale; run go test ./internal/scene -update and re-run the excalidraw-app test", path)
	}
}
