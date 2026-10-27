package boards

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxNameLen  = 200
	DefaultName = "Untitled"

	// Mirrors FILE_UPLOAD_MAX_BYTES in excalidraw-app/app_constants.ts.
	maxFileBytes = 4 << 20
	// A base64 data URL is 4/3 of the payload plus a short "data:...;base64,"
	// prefix.
	maxDataURLLen = maxFileBytes*4/3 + 1024
	maxFileIDs    = 200
)

var (
	uuidPattern   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	fileIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

	// IMAGE_MIME_TYPES plus MIME_TYPES.binary from packages/common/src/constants.ts
	allowedMimeTypes = map[string]bool{
		"image/svg+xml":            true,
		"image/png":                true,
		"image/jpeg":               true,
		"image/gif":                true,
		"image/webp":               true,
		"image/bmp":                true,
		"image/x-icon":             true,
		"image/avif":               true,
		"image/jfif":               true,
		"application/octet-stream": true,
	}
)

// ValidationError is returned for client input the server refuses (400).
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// FileTooLargeError is returned for files over maxFileBytes (413).
type FileTooLargeError struct{}

func (e *FileTooLargeError) Error() string {
	return fmt.Sprintf("file exceeds %d MiB", maxFileBytes>>20)
}

func IsValidationError(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

func validateBoardID(id string) error {
	if !uuidPattern.MatchString(id) {
		return invalid("invalid board id")
	}
	return nil
}

func validateFileID(id string) error {
	if !fileIDPattern.MatchString(id) {
		return invalid("invalid file id %q", id)
	}
	return nil
}

// normalizeName trims the name; an empty name falls back to DefaultName only
// when allowDefault is set (creation), otherwise it is an error (rename).
func normalizeName(name string, allowDefault bool) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" && allowDefault {
		return DefaultName, nil
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return "", invalid("name must be 1-%d characters", maxNameLen)
	}
	return name, nil
}

// normalizeScene accepts {"elements": [...], "appState": {...}} and returns it
// re-serialized with only those two keys, so arbitrary client junk is never
// stored. The element and appState payloads themselves are kept verbatim.
func normalizeScene(raw json.RawMessage) (json.RawMessage, error) {
	var scene struct {
		Elements json.RawMessage `json:"elements"`
		AppState json.RawMessage `json:"appState"`
	}
	if err := json.Unmarshal(raw, &scene); err != nil {
		return nil, invalid("scene must be a JSON object")
	}
	if !startsWith(scene.Elements, '[') {
		return nil, invalid("scene.elements must be an array")
	}
	if len(scene.AppState) == 0 || string(bytes.TrimSpace(scene.AppState)) == "null" {
		scene.AppState = json.RawMessage(`{}`)
	} else if !startsWith(scene.AppState, '{') {
		return nil, invalid("scene.appState must be an object")
	}
	// json.Marshal would rewrite <, > and & inside the payload as < etc.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(scene); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func startsWith(raw json.RawMessage, c byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == c
}

func validateFile(f File) error {
	if err := validateFileID(f.ID); err != nil {
		return err
	}
	if !allowedMimeTypes[f.MimeType] {
		return invalid("unsupported mime type %q", f.MimeType)
	}
	// "data:<mime>," or "data:<mime>;base64," (or other parameters)
	prefix := "data:" + f.MimeType
	if !strings.HasPrefix(f.DataURL, prefix) ||
		len(f.DataURL) == len(prefix) ||
		(f.DataURL[len(prefix)] != ',' && f.DataURL[len(prefix)] != ';') {
		return invalid("dataURL must be a data URL of type %s", f.MimeType)
	}
	if len(f.DataURL) > maxDataURLLen {
		return &FileTooLargeError{}
	}
	if f.Created <= 0 {
		return invalid("created must be a positive epoch milliseconds timestamp")
	}
	if f.Version < 1 {
		return invalid("version must be >= 1")
	}
	return nil
}
