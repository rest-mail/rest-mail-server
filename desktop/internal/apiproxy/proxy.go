package apiproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
)

const maxResponseSize = 64 << 20

type Manager interface {
	Status() session.SessionStatus
	DoRequest(context.Context, *http.Request) (*http.Response, error)
	Redact([]byte) []byte
}

func New(manager Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if !strings.HasPrefix(request.URL.Path, "/api/") && !strings.HasPrefix(request.URL.Path, "/restmail/") {
				next.ServeHTTP(writer, request)
				return
			}
			if !manager.Status().SignedIn {
				fail(writer, http.StatusUnauthorized)
				return
			}
			for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/refresh", "/api/v1/auth/logout"} {
				if strings.TrimSuffix(request.URL.Path, "/") == path {
					fail(writer, http.StatusForbidden)
					return
				}
			}
			response, err := manager.DoRequest(request.Context(), request)
			if err != nil {
				status := http.StatusBadGateway
				var apiError *session.APIError
				if errors.Is(err, session.ErrSignedOut) || errors.As(err, &apiError) && apiError.Status == http.StatusUnauthorized {
					status = http.StatusUnauthorized
				}
				fail(writer, status)
				return
			}
			defer response.Body.Close()
			if response.StatusCode >= 300 && response.StatusCode < 400 || response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity" {
				fail(writer, http.StatusBadGateway)
				return
			}
			if session.IsEventStream(response.Header.Get("Content-Type")) {
				stream(writer, response, manager)
				return
			}
			body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
			if err != nil || len(body) > maxResponseSize {
				fail(writer, http.StatusBadGateway)
				return
			}
			if len(body) != 0 && (strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "json") || json.Valid(body)) {
				var value any
				decoder := json.NewDecoder(bytes.NewReader(body))
				decoder.UseNumber()
				if !json.Valid(body) || decoder.Decode(&value) != nil {
					fail(writer, http.StatusBadGateway)
					return
				}
				body, err = json.Marshal(sanitize(value, manager))
				if err != nil {
					fail(writer, http.StatusBadGateway)
					return
				}
			}
			body = manager.Redact(body)
			for _, header := range []string{"Content-Type", "Content-Disposition", "Last-Modified"} {
				for _, value := range response.Header.Values(header) {
					writer.Header().Add(header, string(manager.Redact([]byte(value))))
				}
			}
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("X-Content-Type-Options", "nosniff")
			writer.WriteHeader(response.StatusCode)
			_, _ = writer.Write(body)
		})
	}
}

// stream relays a server-sent event stream one line at a time, so events reach
// the webview while the stream stays open. Each data line gets the same
// credential stripping and redaction as a buffered response; credentials hold no
// newline, so a line never splits one.
func stream(writer http.ResponseWriter, response *http.Response, manager Manager) {
	flusher, canFlush := writer.(http.Flusher)
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(response.StatusCode)
	if canFlush {
		flusher.Flush()
	}
	reader := bufio.NewReaderSize(response.Body, 64<<10)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return
		}
		if len(line) != 0 {
			if _, writeErr := writer.Write(streamLine(line, manager)); writeErr != nil {
				return
			}
			if canFlush && (len(bytes.TrimRight(line, "\r\n")) == 0 || err != nil) {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func streamLine(line []byte, manager Manager) []byte {
	ending := line[len(bytes.TrimRight(line, "\r\n")):]
	content := line[:len(line)-len(ending)]
	if payload, ok := bytes.CutPrefix(content, []byte("data:")); ok {
		trimmed := bytes.TrimPrefix(payload, []byte(" "))
		if json.Valid(trimmed) {
			var value any
			decoder := json.NewDecoder(bytes.NewReader(trimmed))
			decoder.UseNumber()
			if decoder.Decode(&value) == nil {
				if encoded, err := json.Marshal(sanitize(value, manager)); err == nil {
					content = append([]byte("data: "), encoded...)
				}
			}
		}
	}
	return append(manager.Redact(append([]byte(nil), content...)), ending...)
}

func sanitize(value any, manager Manager) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			normalized := strings.ToLower(key)
			if strings.Contains(normalized, "token") || strings.Contains(normalized, "credential") || strings.Contains(normalized, "password") || strings.Contains(normalized, "secret") {
				delete(typed, key)
			} else {
				typed[key] = sanitize(nested, manager)
			}
		}
	case []any:
		for index, nested := range typed {
			typed[index] = sanitize(nested, manager)
		}
	case string:
		return string(manager.Redact([]byte(typed)))
	}
	return value
}

func fail(writer http.ResponseWriter, status int) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]string{"code": http.StatusText(status), "message": http.StatusText(status)}})
}
