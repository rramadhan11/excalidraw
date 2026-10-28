package boards

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/apperr"
)

func isInvalid(err error) bool { return apperr.Is(err, http.StatusBadRequest) }

func TestNormalizeScene(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"keeps elements and appState", `{"elements":[{"id":"a"}],"appState":{"viewBackgroundColor":"#fff"}}`, `{"elements":[{"id":"a"}],"appState":{"viewBackgroundColor":"#fff"}}`, false},
		{"drops unknown keys", `{"elements":[],"appState":{},"files":{"x":1},"junk":true}`, `{"elements":[],"appState":{}}`, false},
		{"defaults missing appState", `{"elements":[]}`, `{"elements":[],"appState":{}}`, false},
		{"defaults null appState", `{"elements":[],"appState":null}`, `{"elements":[],"appState":{}}`, false},
		{"keeps html characters and NUL escapes verbatim", `{"elements":[{"text":"<a> & \u0000"}],"appState":{}}`, `{"elements":[{"text":"<a> & \u0000"}],"appState":{}}`, false},
		{"rejects missing elements", `{"appState":{}}`, "", true},
		{"rejects non-array elements", `{"elements":{}}`, "", true},
		{"rejects non-object appState", `{"elements":[],"appState":[]}`, "", true},
		{"rejects non-object scene", `[]`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeScene(json.RawMessage(tt.in))
			if tt.wantErr {
				if !isInvalid(err) {
					t.Fatalf("want validation error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestNormalizeName(t *testing.T) {
	if got, _ := normalizeName("  ", true); got != DefaultName {
		t.Fatalf("empty name on create: got %q", got)
	}
	if _, err := normalizeName("  ", false); !isInvalid(err) {
		t.Fatalf("empty name on rename should be rejected, got %v", err)
	}
	if got, _ := normalizeName("  Plan  ", false); got != "Plan" {
		t.Fatalf("got %q", got)
	}
	if _, err := normalizeName(strings.Repeat("é", maxNameLen), false); err != nil {
		t.Fatalf("limit counts characters, not bytes: %v", err)
	}
	if _, err := normalizeName(strings.Repeat("a", maxNameLen+1), false); !isInvalid(err) {
		t.Fatalf("too long name should be rejected, got %v", err)
	}
}

func TestValidateFile(t *testing.T) {
	valid := File{ID: "abc123", MimeType: "image/png", DataURL: "data:image/png;base64,iVBORw0KGgo=", Created: 1, Version: 1}

	if err := validateFile(valid); err != nil {
		t.Fatalf("valid file rejected: %v", err)
	}
	svg := valid
	svg.MimeType, svg.DataURL = "image/svg+xml", "data:image/svg+xml,%3Csvg%3E"
	if err := validateFile(svg); err != nil {
		t.Fatalf("non-base64 data URL rejected: %v", err)
	}

	cases := map[string]func(f *File){
		"bad id":             func(f *File) { f.ID = "../etc" },
		"unknown mime":       func(f *File) { f.MimeType = "text/html"; f.DataURL = "data:text/html,x" },
		"mismatched dataURL": func(f *File) { f.DataURL = "data:image/jpeg;base64,xx" },
		"mime prefix only":   func(f *File) { f.DataURL = "data:image/pngx;base64,xx" },
		"not a data URL":     func(f *File) { f.DataURL = "https://example.com/a.png" },
		"zero created":       func(f *File) { f.Created = 0 },
		"zero version":       func(f *File) { f.Version = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := valid
			mutate(&f)
			if err := validateFile(f); !isInvalid(err) {
				t.Fatalf("want validation error, got %v", err)
			}
		})
	}

	big := valid
	big.DataURL = "data:image/png;base64," + strings.Repeat("A", maxDataURLLen)
	if err := validateFile(big); !apperr.Is(err, http.StatusRequestEntityTooLarge) {
		t.Fatalf("want 413, got %v", err)
	}
}

func TestValidateThumbnail(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3}
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n', 0}

	if err := validateThumbnail("image/jpeg", jpeg); err != nil {
		t.Fatal(err)
	}
	if err := validateThumbnail("image/png", png); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		mime string
		data []byte
	}{
		"mime/content mismatch": {"image/png", jpeg},
		"html disguised as png": {"image/png", []byte("<html><script>")},
		"unsupported type":      {"image/svg+xml", []byte("<svg/>")},
		"empty":                 {"image/jpeg", nil},
	} {
		if err := validateThumbnail(tc.mime, tc.data); !isInvalid(err) {
			t.Errorf("%s: want 400, got %v", name, err)
		}
	}
	big := append(append([]byte{}, jpeg...), make([]byte, maxThumbnailBytes)...)
	if err := validateThumbnail("image/jpeg", big); !apperr.Is(err, http.StatusRequestEntityTooLarge) {
		t.Fatalf("want 413, got %v", err)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	c := cursor{
		UpdatedAt: time.Date(2026, 10, 28, 10, 11, 12, 123456000, time.UTC),
		ID:        "11111111-1111-4111-8111-111111111111",
	}
	got, err := decodeCursor(c.encode())
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(c.UpdatedAt) || got.ID != c.ID {
		t.Fatalf("got %+v, want %+v", got, c)
	}
	if got, err := decodeCursor(""); got != nil || err != nil {
		t.Fatalf("empty cursor: %v %v", got, err)
	}
	for _, bad := range []string{"%%%", "bm9waXBl", "MjAyNnxub3QtYS11dWlk"} {
		if _, err := decodeCursor(bad); !isInvalid(err) {
			t.Errorf("%q: want 400, got %v", bad, err)
		}
	}
}

func TestParseCollectionFilter(t *testing.T) {
	if f, _ := parseCollectionFilter(""); f != (CollectionFilter{}) {
		t.Fatalf("empty: %+v", f)
	}
	if f, _ := parseCollectionFilter("unsorted"); !f.Unsorted {
		t.Fatalf("unsorted: %+v", f)
	}
	id := "11111111-1111-4111-8111-111111111111"
	if f, _ := parseCollectionFilter(id); f.CollectionID != id {
		t.Fatalf("id: %+v", f)
	}
	if _, err := parseCollectionFilter("drop table"); !isInvalid(err) {
		t.Fatalf("want 400, got %v", err)
	}
}

func TestParseUpdate(t *testing.T) {
	name := "Plan"
	u, _ := parseUpdate(updateRequest{Name: &name})
	if u.MoveToCollection || *u.Name != "Plan" {
		t.Fatalf("absent collectionId must not move: %+v", u)
	}
	u, _ = parseUpdate(updateRequest{CollectionID: json.RawMessage("null")})
	if !u.MoveToCollection || u.CollectionID != nil {
		t.Fatalf("null collectionId must move to unsorted: %+v", u)
	}
	u, _ = parseUpdate(updateRequest{CollectionID: json.RawMessage(`"abc"`)})
	if !u.MoveToCollection || *u.CollectionID != "abc" {
		t.Fatalf("string collectionId: %+v", u)
	}
	if _, err := parseUpdate(updateRequest{CollectionID: json.RawMessage(`42`)}); !isInvalid(err) {
		t.Fatalf("want 400, got %v", err)
	}
}
