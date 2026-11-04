package metrics

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

// definedMetricNames is every restmail_* metric name this package defines,
// read from its source: the Name of each metric and of each scrape-time Desc.
func definedMetricNames(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	name := regexp.MustCompile(`"(restmail_[a-z0-9_]+)"`)
	names := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range name.FindAllStringSubmatch(string(src), -1) {
			names[m[1]] = true
		}
	}
	return names
}

// alertRules reads monitoring/alerting-rules.yml as alert name to expression.
func alertRules(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../../monitoring/alerting-rules.yml")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Groups []struct {
			Rules []struct {
				Alert string `yaml:"alert"`
				Expr  string `yaml:"expr"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	rules := map[string]string{}
	for _, g := range file.Groups {
		for _, r := range g.Rules {
			rules[r.Alert] = strings.Join(strings.Fields(r.Expr), " ")
		}
	}
	return rules
}

// TestAlertRules_ReadExistingMetrics: every restmail_* metric an alert in
// monitoring/alerting-rules.yml reads is one this package defines (a
// histogram's _bucket, _sum and _count series included), so no alert waits on
// a series that never exists. The alerts #1579 added are checked by name, and
// each reads the metric it was written for.
func TestAlertRules_ReadExistingMetrics(t *testing.T) {
	defined := definedMetricNames(t)
	rules := alertRules(t)
	ref := regexp.MustCompile(`restmail_[a-z0-9_]+`)
	for alert, expr := range rules {
		for _, m := range ref.FindAllString(expr, -1) {
			base := m
			for _, suffix := range []string{"_bucket", "_sum", "_count"} {
				if trimmed, ok := strings.CutSuffix(m, suffix); ok && defined[trimmed] {
					base = trimmed
				}
			}
			if !defined[base] {
				t.Errorf("%s reads %s, which internal/metrics does not define", alert, m)
			}
		}
	}

	for alert, metric := range map[string]string{
		"QueueOldestMessageOld":       "restmail_queue_oldest_age_seconds",
		"DatabaseConnectionErrors":    "restmail_db_connection_errors_total",
		"DatabasePoolSaturated":       "restmail_db_pool_in_use_connections",
		"PipelineFilterErrorRateHigh": `restmail_pipeline_stage_decisions_total{action="error"}`,
		"SMTPConnectionsStopped":      `restmail_connections_accepted_total{protocol="smtp"}`,
		"IMAPConnectionsStopped":      `restmail_connections_accepted_total{protocol="imap"}`,
		"DKIMKeyOverdue":              "restmail_dkim_key_age_seconds",
		"DKIMPublicationStuck":        "restmail_dkim_rotation_step_started_seconds",
		"CertificateExpiringSoon":     "restmail_cert_expiry_days < 14",
	} {
		expr, ok := rules[alert]
		if !ok {
			t.Errorf("no %s rule in monitoring/alerting-rules.yml", alert)
			continue
		}
		if !strings.Contains(expr, metric) {
			t.Errorf("%s expr %q does not read %s", alert, expr, metric)
		}
	}
}
