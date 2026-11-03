package mtls

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestChartGatewayNamesMatch checks the Helm chart issues each gateway service
// the certificate name the API knows it by: restmail.internalMtls.gateways in
// the chart lists exactly Gateways.
func TestChartGatewayNamesMatch(t *testing.T) {
	data, err := os.ReadFile("../../helm/restmail/templates/_helpers.tpl")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)\{\{- define "restmail\.internalMtls\.gateways" -\}\}\n(.*?)\{\{- end -\}\}`).FindSubmatch(data)
	if block == nil {
		t.Fatal(`no "restmail.internalMtls.gateways" define in the chart's _helpers.tpl`)
	}
	chart := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(block[1])), "\n") {
		service, cn, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("chart gateway line %q is not service: name", line)
		}
		chart[strings.TrimSpace(service)] = strings.TrimSpace(cn)
	}
	if len(chart) != len(Gateways) {
		t.Errorf("chart lists %d gateway services, Gateways %d: %v", len(chart), len(Gateways), chart)
	}
	for _, g := range Gateways {
		if chart[g.Service] != g.CommonName {
			t.Errorf("chart names %s %q, want %q", g.Service, chart[g.Service], g.CommonName)
		}
	}
}
