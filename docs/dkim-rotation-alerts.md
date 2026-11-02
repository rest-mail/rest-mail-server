# DKIM rotation alerts

The API's network-restricted `/metrics` endpoint exports these scrape-time
gauges alongside `restmail_dkim_key_age_seconds{domain,selector}`:

| Metric | Labels | Meaning |
| --- | --- | --- |
| `restmail_dkim_rotation_interval_seconds` | `domain` | Configured lifetime in seconds; absent when rotation is disabled. |
| `restmail_dkim_rotation_state` | `domain,state` | One series with value 1 for the current state: `stable`, `awaiting_publication`, `waiting_ttl`, or `retiring`. |
| `restmail_dkim_rotation_step_started_seconds` | `domain` | Unix timestamp of staging for `awaiting_publication`, first publication for `waiting_ttl`, or the signing switch for `retiring`; absent for `stable`. |

States cover configured domains, including disabled domains with a rotation
already in progress. Labels never contain keys, record values or a history of
selectors. Collecting these metrics does not load or decrypt private keys.
Unknown legacy key ages remain absent, not zero, so the overdue alert cannot
evaluate them. Consult [DKIM key age](dkim-key-age.md) and the rotation status
endpoint for the record to publish and retirement/revocation deadlines.

## Plain Prometheus

Save this as a rule file and include it in Prometheus's `rule_files`. The
expressions are checked against the chart by its render tests. They match the
same domain, scrape job and instance, so multiple API replicas do not cause a
many-to-many match or mix separate deployments' policies.

```yaml
groups:
  - name: restmail-dkim
    rules:
      - alert: DKIMKeyOverdue
        expr: restmail_dkim_key_age_seconds > on(domain, job, instance) group_left() (restmail_dkim_rotation_interval_seconds + 2 * 24 * 60 * 60)
        labels:
          severity: warning
        annotations:
          summary: "DKIM key for {{ $labels.domain }} is overdue"
          description: "The current key exceeds its configured rotation interval plus two days. Check the rotation status and DNS records."
      - alert: DKIMPublicationStuck
        expr: (time() - restmail_dkim_rotation_step_started_seconds > 24 * 60 * 60) and on(domain, job, instance) (restmail_dkim_rotation_state{state="awaiting_publication"} == 1)
        labels:
          severity: warning
        annotations:
          summary: "DKIM publication for {{ $labels.domain }} is stuck"
          description: "The staged record has not resolved for more than 24 hours. Publish the TXT record reported by the rotation status endpoint."
```

The overdue threshold is strictly greater than the interval plus two 24-hour
days. Publication is stuck only while `awaiting_publication` and strictly
more than 24 hours after staging. No additional `for` delay is applied.
The worker follows observed DNS, not provider submissions; with manual DNS,
the publication alert is the operator's cue to publish the reported TXT record.

The existing `monitoring/prometheus.yml` already loads
`monitoring/alerting-rules.yml`. For that deployment, add the group above to
that file's `groups` list; do not replace the existing alerts.

## Helm with Prometheus Operator

The chart emits no Operator resource by default. With the Prometheus Operator
CRD already installed, enable the two alerts on an existing release using:

```sh
helm upgrade restmail ./helm/restmail --reuse-values \
  --set metrics.prometheusRule.enabled=true \
  --set metrics.prometheusRule.labels.release=monitoring
```

Choose labels that match your Prometheus rule selector; `release=monitoring`
is only an example. This option does not install the Operator or a
ServiceMonitor and does not change scrape targets or network access. Configure
scraping of the API's existing `/metrics` endpoint separately. Disabling the
option removes the `PrometheusRule` from the rendered chart.
