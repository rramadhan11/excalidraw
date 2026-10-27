package boards

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

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
				if !IsValidationError(err) {
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
	if _, err := normalizeName("  ", false); !IsValidationError(err) {
		t.Fatalf("empty name on rename should be rejected, got %v", err)
	}
	if got, _ := normalizeName("  Plan  ", false); got != "Plan" {
		t.Fatalf("got %q", got)
	}
	if _, err := normalizeName(strings.Repeat("é", maxNameLen), false); err != nil {
		t.Fatalf("limit counts characters, not bytes: %v", err)
	}
	if _, err := normalizeName(strings.Repeat("a", maxNameLen+1), false); !IsValidationError(err) {
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
			if err := validateFile(f); !IsValidationError(err) {
				t.Fatalf("want validation error, got %v", err)
			}
		})
	}

	big := valid
	big.DataURL = "data:image/png;base64," + strings.Repeat("A", maxDataURLLen)
	var tooLarge *FileTooLargeError
	if err := validateFile(big); !errors.As(err, &tooLarge) {
		t.Fatalf("want FileTooLargeError, got %v", err)
	}
}
