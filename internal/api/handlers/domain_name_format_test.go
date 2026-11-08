package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDomainNameDNSLengths(t *testing.T) {
	for _, name := range []string{
		"bad..example",
		".example.com",
		strings.Repeat("a", 64) + ".example",
		"example." + strings.Repeat("a", 64),
		strings.Repeat("a.", 126) + "aa",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateDomainName(name); err == nil {
				t.Fatal("accepted an invalid DNS hostname")
			}
			body, err := json.Marshal(map[string]string{"name": name})
			if err != nil {
				t.Fatal(err)
			}
			rr, reached := createDomainNoDB(t, string(body))
			if reached || rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("database reached=%v, status=%d, body=%s", reached, rr.Code, rr.Body)
			}
			var response struct {
				Error struct {
					Code    string `json:"code"`
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error.Code != "validation_failed" || response.Error.Details.Fields["name"] == "" {
				t.Fatalf("not a name validation error: %s", rr.Body)
			}
		})
	}
}

func TestDomainNameIDNAndDNSBoundaries(t *testing.T) {
	for name, want := range map[string]string{
		"例子.广告":                              "xn--fsqu00a.xn--4rr70v",
		"例子.卷筒纸":                             "xn--fsqu00a.xn--3lr804guic",
		"xn--fsqu00a.xn--3lr804guic":         "xn--fsqu00a.xn--3lr804guic",
		"FAß.Example":                        "xn--fa-hia.example",
		strings.Repeat("a", 63) + ".example": strings.Repeat("a", 63) + ".example",
		strings.Repeat("a.", 126) + "a":      strings.Repeat("a.", 126) + "a",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := validateDomainName(name)
			if err != nil || got != want {
				t.Fatalf("got %q, %v; want %q", got, err, want)
			}
		})
	}
}
