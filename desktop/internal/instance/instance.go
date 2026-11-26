package instance

import (
	"os"
	"regexp"
	"strings"
)

var namePattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func Name(value string) string {
	value = strings.TrimSpace(value)
	if !namePattern.MatchString(value) {
		return ""
	}
	return value
}

func KeychainService() string {
	if name := Name(os.Getenv("RESTMAIL_DESKTOP_INSTANCE")); name != "" {
		return "REST Mail test copy " + name
	}
	return "REST Mail"
}
