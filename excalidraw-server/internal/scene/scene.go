// Package scene reads and edits stored Excalidraw scenes on the server, for
// clients that can't run the editor (the MCP endpoint).
//
// Elements are kept as generic JSON objects so that fields this package does
// not know about survive a round trip untouched. New elements are built to
// match what the editor itself creates (packages/element/src/newElement.ts),
// and the result must load cleanly through restoreElements
// (packages/excalidraw/data/restore.ts); excalidraw-app/tests/mcpScene.test.ts
// checks that against fixtures written by this package's tests.
package scene

import (
	"bytes"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

// InputError is a problem with what the caller asked for (as opposed to a
// server fault); its message is meant to be shown to the caller.
type InputError struct{ msg string }

func (e *InputError) Error() string { return e.msg }

func inputErr(format string, args ...any) error {
	return &InputError{msg: fmt.Sprintf(format, args...)}
}

// IsInputError reports whether err is the caller's fault.
func IsInputError(err error) bool {
	var ie *InputError
	return errors.As(err, &ie)
}

// Element is one Excalidraw element as stored.
type Element map[string]any

func (e Element) str(key string) string {
	s, _ := e[key].(string)
	return s
}

func (e Element) num(key string) float64 {
	switch v := e[key].(type) {
	case float64:
		return v
	case json.Number:
		f, _ := v.Float64()
		return f
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

func (e Element) ID() string          { return e.str("id") }
func (e Element) Type() string        { return e.str("type") }
func (e Element) Deleted() bool       { b, _ := e["isDeleted"].(bool); return b }
func (e Element) ContainerID() string { return e.str("containerId") }

// boundRefs returns the element's boundElements as {type, id} pairs.
func (e Element) boundRefs() []boundRef {
	list, _ := e["boundElements"].([]any)
	refs := make([]boundRef, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		typ, _ := m["type"].(string)
		if id != "" {
			refs = append(refs, boundRef{Type: typ, ID: id})
		}
	}
	return refs
}

func (e Element) setBoundRefs(refs []boundRef) {
	list := make([]any, len(refs))
	for i, r := range refs {
		list[i] = map[string]any{"type": r.Type, "id": r.ID}
	}
	e["boundElements"] = list
}

type boundRef struct {
	Type string
	ID   string
}

// binding returns the elementId and fixedPoint of an arrow's start or end
// binding, if any.
func (e Element) binding(key string) (id string, fx, fy float64, ok bool) {
	m, isMap := e[key].(map[string]any)
	if !isMap {
		return "", 0, 0, false
	}
	id, _ = m["elementId"].(string)
	if id == "" {
		return "", 0, 0, false
	}
	fx, fy = 0.5, 0.5
	if fp, isList := m["fixedPoint"].([]any); isList && len(fp) == 2 {
		fx, fy = toFloat(fp[0]), toFloat(fp[1])
	}
	return id, fx, fy, true
}

func toFloat(v any) float64 {
	return Element{"v": v}.num("v")
}

// Scene is a parsed board scene.
type Scene struct {
	Elements []Element
	// AppState is passed through unchanged.
	AppState json.RawMessage
	byID     map[string]Element
	ed       *editor
}

// Parse decodes a stored scene ({"elements": [...], "appState": {...}}).
// Numbers are kept as json.Number so untouched values round-trip exactly.
func Parse(raw json.RawMessage) (*Scene, error) {
	var stored struct {
		Elements []Element       `json:"elements"`
		AppState json.RawMessage `json:"appState"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&stored); err != nil {
		return nil, fmt.Errorf("decode scene: %w", err)
	}
	s := &Scene{Elements: stored.Elements, AppState: stored.AppState, ed: newEditor()}
	if s.Elements == nil {
		s.Elements = []Element{}
	}
	s.reindex()
	return s, nil
}

// Marshal encodes the scene in the stored format.
func (s *Scene) Marshal() (json.RawMessage, error) {
	appState := s.AppState
	if len(appState) == 0 {
		appState = json.RawMessage(`{}`)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// labels may legitimately contain <, > and &
	enc.SetEscapeHTML(false)
	err := enc.Encode(struct {
		Elements []Element       `json:"elements"`
		AppState json.RawMessage `json:"appState"`
	}{s.Elements, appState})
	if err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func (s *Scene) reindex() {
	s.byID = make(map[string]Element, len(s.Elements))
	for _, el := range s.Elements {
		if id := el.ID(); id != "" {
			s.byID[id] = el
		}
	}
}

// live returns a non-deleted element by id.
func (s *Scene) live(id string) (Element, bool) {
	el, ok := s.byID[id]
	if !ok || el.Deleted() {
		return nil, false
	}
	return el, true
}

func (s *Scene) append(el Element) {
	s.Elements = append(s.Elements, el)
	s.byID[el.ID()] = el
}

// editor supplies the randomness and clock for element metadata; tests
// replace it to get deterministic output.
type editor struct {
	rng *rand.Rand
	now func() time.Time
}

func newEditor() *editor {
	var seed [32]byte
	if _, err := crand.Read(seed[:]); err != nil {
		panic(err)
	}
	return &editor{rng: rand.New(rand.NewChaCha8(seed)), now: time.Now}
}

// WithDeterminism makes ids, seeds and timestamps reproducible (tests only).
func (s *Scene) WithDeterminism(seed uint64, now time.Time) {
	var b [32]byte
	binary.LittleEndian.PutUint64(b[:], seed)
	s.ed = &editor{rng: rand.New(rand.NewChaCha8(b)), now: func() time.Time { return now }}
}

const idAlphabet = "useandom-26T198340PX75pxJACKVERYMINDBUSHWOLF_GQZbfghjklqvwyzrict"

// newID mirrors randomId() (nanoid: 21 url-safe characters).
func (ed *editor) newID() string {
	b := make([]byte, 21)
	for i := range b {
		b[i] = idAlphabet[ed.rng.IntN(len(idAlphabet))]
	}
	return string(b)
}

// randomInteger mirrors randomInteger() in packages/common/src/random.ts.
func (ed *editor) randomInteger() int64 {
	return ed.rng.Int64N(math.MaxInt32)
}

func (ed *editor) timestamp() int64 {
	return ed.now().UnixMilli()
}

// touchAll touches the given elements in scene order, so the random nonces
// they get don't depend on map iteration order.
func (s *Scene) touchAll(set map[string]Element) {
	for _, el := range s.Elements {
		if _, ok := set[el.ID()]; ok {
			s.touch(el)
		}
	}
}

// touch marks an element as modified the way the editor's mutateElement does,
// so version-based reconciliation sees the change.
func (s *Scene) touch(el Element) {
	el["version"] = int64(el.num("version")) + 1
	el["versionNonce"] = s.ed.randomInteger()
	el["updated"] = s.ed.timestamp()
}
