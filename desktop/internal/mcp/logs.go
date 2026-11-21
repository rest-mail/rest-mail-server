package mcp

import (
	"regexp"
	"strings"
	"sync"
)

var credentials = regexp.MustCompile(`(?i)(?:authorization\s*[:=]\s*)?bearer\s+[A-Za-z0-9._~+/=-]+|(?:access[_-]?token|refresh[_-]?token|password|authorization|token)\s*[=:]\s*[^\s,;]+`)

func redactCredentials(text string) string {
	return credentials.ReplaceAllString(text, "[REDACTED]")
}

func scrubFields(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			if strings.Contains(normalized, "token") || normalized == "password" || normalized == "authorization" || normalized == "secret" {
				typed[key] = "[REDACTED]"
			} else {
				typed[key] = scrubFields(child)
			}
		}
	case []any:
		for index, child := range typed {
			typed[index] = scrubFields(child)
		}
	}
	return value
}

func redactStrings(value any, redact func(string) string) any {
	switch typed := value.(type) {
	case string:
		return redact(typed)
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			result[redact(key)] = redactStrings(child, redact)
		}
		return result
	case []any:
		for index, child := range typed {
			typed[index] = redactStrings(child, redact)
		}
	}
	return value
}

type LogBuffer struct {
	mutex    sync.Mutex
	lines    []string
	capacity int
	redact   func(string) string
}

func NewLogBuffer(capacity int, redact func(string) string) *LogBuffer {
	if capacity < 1 {
		capacity = 1
	}
	if redact == nil {
		redact = redactCredentials
	}
	return &LogBuffer{capacity: capacity, redact: redact}
}

func (buffer *LogBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	for _, line := range strings.Split(strings.TrimRight(buffer.redact(string(data)), "\r\n"), "\n") {
		if len(line) > 8192 {
			line = line[:8192]
		}
		buffer.lines = append(buffer.lines, line)
		if len(buffer.lines) > buffer.capacity {
			buffer.lines = buffer.lines[1:]
		}
	}
	return len(data), nil
}

func (buffer *LogBuffer) Recent(lines int) []string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	if lines <= 0 {
		lines = 100
	}
	if lines > len(buffer.lines) {
		lines = len(buffer.lines)
	}
	result := make([]string, lines)
	for index, line := range buffer.lines[len(buffer.lines)-lines:] {
		result[index] = buffer.redact(line)
	}
	return result
}
