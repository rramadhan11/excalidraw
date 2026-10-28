package boards

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/access"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/apperr"
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

	maxThumbnailBytes = 512 << 10

	defaultPageSize = 60
	maxPageSize     = 200
)

var (
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

	// thumbnails are served from our origin, so only these exact formats are
	// accepted, verified by their magic bytes
	thumbnailSignatures = map[string][]byte{
		"image/jpeg": {0xFF, 0xD8, 0xFF},
		"image/png":  {0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'},
	}
)

func validateFileID(id string) error {
	if !fileIDPattern.MatchString(id) {
		return apperr.Invalid("invalid file id %q", id)
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
		return "", apperr.Invalid("name must be 1-%d characters", maxNameLen)
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
		return nil, apperr.Invalid("scene must be a JSON object")
	}
	if !startsWith(scene.Elements, '[') {
		return nil, apperr.Invalid("scene.elements must be an array")
	}
	if len(scene.AppState) == 0 || string(bytes.TrimSpace(scene.AppState)) == "null" {
		scene.AppState = json.RawMessage(`{}`)
	} else if !startsWith(scene.AppState, '{') {
		return nil, apperr.Invalid("scene.appState must be an object")
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
		return apperr.Invalid("unsupported mime type %q", f.MimeType)
	}
	// "data:<mime>," or "data:<mime>;base64," (or other parameters)
	prefix := "data:" + f.MimeType
	if !strings.HasPrefix(f.DataURL, prefix) ||
		len(f.DataURL) == len(prefix) ||
		(f.DataURL[len(prefix)] != ',' && f.DataURL[len(prefix)] != ';') {
		return apperr.Invalid("dataURL must be a data URL of type %s", f.MimeType)
	}
	if len(f.DataURL) > maxDataURLLen {
		return apperr.TooLarge("file exceeds 4 MiB")
	}
	if f.Created <= 0 {
		return apperr.Invalid("created must be a positive epoch milliseconds timestamp")
	}
	if f.Version < 1 {
		return apperr.Invalid("version must be >= 1")
	}
	return nil
}

func validateThumbnail(mimeType string, data []byte) error {
	signature, ok := thumbnailSignatures[mimeType]
	if !ok {
		return apperr.Invalid("thumbnail must be image/jpeg or image/png")
	}
	if len(data) > maxThumbnailBytes {
		return apperr.TooLarge("thumbnail exceeds 512 KiB")
	}
	if !bytes.HasPrefix(data, signature) {
		return apperr.Invalid("thumbnail content is not a valid %s", mimeType)
	}
	return nil
}

// ---------------------------------------------------------------------------
// listing

// CollectionFilter narrows a board listing: all boards, unsorted ones, or one
// collection.
type CollectionFilter struct {
	Unsorted     bool
	CollectionID string
}

func parseCollectionFilter(raw string) (CollectionFilter, error) {
	switch {
	case raw == "":
		return CollectionFilter{}, nil
	case raw == "unsorted":
		return CollectionFilter{Unsorted: true}, nil
	case access.ValidUUID(raw):
		return CollectionFilter{CollectionID: raw}, nil
	default:
		return CollectionFilter{}, apperr.Invalid("collection must be a collection id or \"unsorted\"")
	}
}

// cursor is the keyset position after the last board of a page, in the
// listing order (updated_at DESC, id DESC). It is opaque to clients.
type cursor struct {
	UpdatedAt time.Time
	ID        string
}

func (c cursor) encode() string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(c.UpdatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID))
}

func decodeCursor(raw string) (*cursor, error) {
	if raw == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, apperr.Invalid("invalid cursor")
	}
	ts, id, ok := strings.Cut(string(b), "|")
	if !ok || !access.ValidUUID(id) {
		return nil, apperr.Invalid("invalid cursor")
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, apperr.Invalid("invalid cursor")
	}
	return &cursor{UpdatedAt: updatedAt, ID: id}, nil
}
