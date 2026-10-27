// Package httpx holds the JSON and middleware plumbing shared by handlers.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"time"
)

// CSRFHeader must be sent with every state-changing request. Browsers do not
// allow cross-origin pages to set custom headers without a CORS preflight,
// which this server never approves.
const (
	CSRFHeader      = "X-Requested-With"
	CSRFHeaderValue = "excalidraw"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON writes v as a JSON response. A nil v writes only the status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	if v == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	// return scene text as stored instead of rewriting <, > and & as <
	// etc.; nosniff keeps browsers from ever treating the body as HTML
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// WriteError writes the API's uniform error shape:
// {"error": {"code": "...", "message": "..."}}.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, errorBody{errorDetail{Code: code, Message: message}})
}

var ErrBodyTooLarge = errors.New("request body too large")

// DecodeJSON decodes exactly one JSON value from the body into dst, rejecting
// unknown fields, trailing data and bodies larger than maxBytes.
func DecodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			return decodeError(err)
		}
		return errors.New("invalid JSON body: unexpected trailing data")
	}
	return nil
}

func decodeError(err error) error {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return ErrBodyTooLarge
	}
	return fmt.Errorf("invalid JSON body: %w", err)
}

// WriteDecodeError maps a DecodeJSON error to 413 or 400.
func WriteDecodeError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrBodyTooLarge) {
		WriteError(w, http.StatusRequestEntityTooLarge, "body_too_large", err.Error())
		return
	}
	WriteError(w, http.StatusBadRequest, "invalid_body", err.Error())
}

// ClientIP returns the caller's IP. X-Real-IP is only honoured when the
// server sits behind a reverse proxy that sets it (trustProxy), otherwise any
// client could spoof it to dodge rate limits.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if ip := r.Header.Get("X-Real-IP"); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------------------------------------------------------------------------
// middleware

type ctxKey int

const logAttrsKey ctxKey = iota

type logAttrs struct {
	mu    sync.Mutex
	attrs []slog.Attr
}

// AddLogAttrs attaches attributes (e.g. user_id) to the request's access log
// line. No-op outside of the Logger middleware.
func AddLogAttrs(ctx context.Context, attrs ...slog.Attr) {
	if la, ok := ctx.Value(logAttrsKey).(*logAttrs); ok {
		la.mu.Lock()
		la.attrs = append(la.attrs, attrs...)
		la.mu.Unlock()
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Logger assigns a request id, recovers panics and writes one structured
// access log line per request. Query strings are not logged.
func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			requestID := newRequestID()
			w.Header().Set("X-Request-ID", requestID)

			la := &logAttrs{}
			r = r.WithContext(context.WithValue(r.Context(), logAttrsKey, la))
			rec := &statusRecorder{ResponseWriter: w}

			defer func() {
				if p := recover(); p != nil {
					if p == http.ErrAbortHandler {
						panic(p)
					}
					logger.Error("panic",
						slog.String("request_id", requestID),
						slog.Any("panic", p),
						slog.String("stack", string(debug.Stack())),
					)
					if rec.status == 0 {
						WriteError(rec, http.StatusInternalServerError, "internal", "internal server error")
					}
				}

				status := rec.status
				if status == 0 {
					status = http.StatusOK
				}
				level := slog.LevelInfo
				if status >= 500 {
					level = slog.LevelError
				}
				la.mu.Lock()
				attrs := append([]slog.Attr{
					slog.String("request_id", requestID),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", status),
					slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				}, la.attrs...)
				la.mu.Unlock()
				logger.LogAttrs(r.Context(), level, "request", attrs...)
			}()

			next.ServeHTTP(rec, r)
		})
	}
}

// RequireCSRFHeader rejects state-changing requests without CSRFHeader.
func RequireCSRFHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.Header.Get(CSRFHeader) != CSRFHeaderValue {
				WriteError(w, http.StatusForbidden, "csrf", "missing "+CSRFHeader+" header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
