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

// alertRules reads helm/restmail/files/alerting-rules.yml as alert name to expression.
func alertRules(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../../helm/restmail/files/alerting-rules.yml")
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

// externalMetrics are the metrics alerts read that another exporter defines,
// each with the scrape that brings it in. Nothing else outside internal/metrics
// may appear in an alert.
var externalMetrics = map[string]string{
	// node-exporter in the compose monitoring stack (tasks/monitoring.yml),
	// scraped as job "node" in monitoring/prometheus.yml.
	"node_filesystem_avail_bytes": "node-exporter",
	"node_filesystem_size_bytes":  "node-exporter",
	// kubelet's volume stats, scraped by the cluster's own Prometheus
	// (kube-prometheus-stack); the chart installs nothing for them.
	"kubelet_volume_stats_available_bytes": "kubelet",
	"kubelet_volume_stats_capacity_bytes":  "kubelet",
}

var (
	// Parts of a PromQL expression that hold label names or values, numbers,
	// durations or strings rather than metric names.
	promqlNonMetric = regexp.MustCompile(`\{[^}]*\}|\[[^\]]*\]|"[^"]*"|\b[0-9][0-9a-zA-Z.]*|\b(?:by|without|on|ignoring|group_left|group_right)\s*\([^)]*\)`)
	promqlIdent     = regexp.MustCompile(`[a-zA-Z_:][a-zA-Z0-9_:]*(\s*\()?`)
	promqlKeywords  = map[string]bool{
		"and": true, "or": true, "unless": true, "bool": true, "offset": true,
		"by": true, "without": true, "on": true, "ignoring": true,
		"group_left": true, "group_right": true, "inf": true, "nan": true,
	}
)

// exprMetrics returns the metric names a PromQL expression reads: every
// identifier that is not a function call, a keyword, a label or a string.
func exprMetrics(expr string) []string {
	var out []string
	for _, m := range promqlIdent.FindAllStringSubmatch(promqlNonMetric.ReplaceAllString(expr, " "), -1) {
		name := strings.TrimRight(strings.TrimSuffix(strings.TrimSpace(m[0]), "("), " ")
		if m[1] != "" || promqlKeywords[strings.ToLower(name)] {
			continue
		}
		out = append(out, name)
	}
	return out
}

// TestAlertRules_ReadExistingMetrics: every metric an alert in
// helm/restmail/files/alerting-rules.yml reads is one internal/metrics defines (a
// histogram's _bucket, _sum and _count series included) or one listed in
// externalMetrics, so no alert waits on a series that never exists. The
// alerts #1579 added are checked by name, and each reads the metric it was
// written for.
func TestAlertRules_ReadExistingMetrics(t *testing.T) {
	defined := definedMetricNames(t)
	rules := alertRules(t)
	for alert, expr := range rules {
		metrics := exprMetrics(expr)
		if len(metrics) == 0 {
			t.Errorf("%s reads no metric the test can find: %q", alert, expr)
		}
		for _, m := range metrics {
			if _, ok := externalMetrics[m]; ok {
				continue
			}
			base := m
			for _, suffix := range []string{"_bucket", "_sum", "_count"} {
				if trimmed, ok := strings.CutSuffix(m, suffix); ok && defined[trimmed] {
					base = trimmed
				}
			}
			if !defined[base] {
				t.Errorf("%s reads %s, which internal/metrics does not define and externalMetrics does not list", alert, m)
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
		"DiskSpaceLow":                "node_filesystem_avail_bytes",
		"DiskSpaceCritical":           "node_filesystem_avail_bytes",
		"PVCSpaceLow":                 "kubelet_volume_stats_available_bytes",
		"PVCSpaceCritical":            "kubelet_volume_stats_available_bytes",
	} {
		expr, ok := rules[alert]
		if !ok {
			t.Errorf("no %s rule in helm/restmail/files/alerting-rules.yml", alert)
			continue
		}
		if !strings.Contains(expr, metric) {
			t.Errorf("%s expr %q does not read %s", alert, expr, metric)
		}
	}
}

// TestExprMetrics: the PromQL reader finds metric names and skips functions,
// keywords, labels, strings and durations.
func TestExprMetrics(t *testing.T) {
	got := exprMetrics(`sum by (filter) (rate(restmail_a_total{action="error"}[10m])) / ignoring(x) restmail_b > on(domain, job) group_left() (restmail_c + 2 * 60) and time() - restmail_d offset 5m`)
	want := []string{"restmail_a_total", "restmail_b", "restmail_c", "restmail_d"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("exprMetrics = %v, want %v", got, want)
	}
}

// TestAlertRules_DiskAlertsCoverTheVolumes: the disk alerts read the volumes
// node-exporter mounts and the chart's PVCs, and node-exporter is scraped.
func TestAlertRules_DiskAlertsCoverTheVolumes(t *testing.T) {
	rules := alertRules(t)
	for _, alert := range []string{"DiskSpaceLow", "DiskSpaceCritical"} {
		if !strings.Contains(rules[alert], `mountpoint=~"/volumes/.+"`) || !strings.Contains(rules[alert], "node_filesystem_size_bytes") {
			t.Errorf("%s does not compare free space with size on /volumes: %q", alert, rules[alert])
		}
	}
	pvcs := regexp.MustCompile(`^(?:.+-(postgres-data|api-attachments)|data-.+-garage-[0-9]+)$`)
	for _, alert := range []string{"PVCSpaceLow", "PVCSpaceCritical"} {
		if !strings.Contains(rules[alert], `persistentvolumeclaim=~".+-(postgres-data|api-attachments)|data-.+-garage-[0-9]+"`) || !strings.Contains(rules[alert], "kubelet_volume_stats_capacity_bytes") {
			t.Errorf("%s does not compare free space with capacity on the chart's PVCs: %q", alert, rules[alert])
		}
	}
	// The PVC names the chart renders for release "restmail" (api-pvc.yaml,
	// postgres-pvc.yaml, garage-statefulset.yaml's volumeClaimTemplate).
	for _, name := range []string{"restmail-postgres-data", "restmail-api-attachments", "data-restmail-garage-0"} {
		if !pvcs.MatchString(name) {
			t.Errorf("PVC %s is not matched by the PVC alerts", name)
		}
	}
	scrape, err := os.ReadFile("../../monitoring/prometheus.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(scrape), "job_name: 'node'") || !strings.Contains(string(scrape), "node-exporter:9100") {
		t.Error(`monitoring/prometheus.yml does not scrape node-exporter as job "node"`)
	}
}
