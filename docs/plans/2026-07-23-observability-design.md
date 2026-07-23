# Message-Lifecycle Observability ("Prometheus for Email") — Design

Grounded in `rest-mail` @ `98cf7eb`. Citations are `file:line`.

## Decisions & status (2026-07-23)
- **Self-contained is the posture.** DB raw events → a **rollup worker** → condensed DB aggregate tables are the system of record the in-app dashboard reads; a deployer needs NO external Prometheus/Grafana. Prometheus `client_golang` counters are *also* emitted as an **optional** live-ops layer (scrape if you want). (This overrides the agent draft's lean on external Prometheus for history — user explicitly wants self-contained rollups.)
- **Three data paths:** (a) Prometheus counters = optional live rates/alerting; (b) DB raw per-message traces (≤7d hot) = "what happened to THIS message" + source for rollups; (c) DB rollups (long-term, condensed) = durable windowed analytics.
- **Always-on vs knob:** aggregate counts (received/rejected/quarantined/auth-fail/reason breakdown) are ALWAYS-ON, never disable-able. The per-message trace detail is the volume/cost dial (retention + sampling). Consistent with secure-by-construction: you can't turn off the security signal, only the storage-costly per-message detail.
- **Self-decided defaults:** custom filter names collapse to label `"custom"`; add a dedicated `CapTracesRead`/`CapStatsRead` RBAC capability (dashboard stats are currently ungated — a gap); 7-day raw window; anomalies (non-continue) kept 100%, happy-path sampled.
- **RESOLVED (user, 2026-07-23):** PII = **store raw, pruned at the 7-day window** (full forensics while hot; only anonymous aggregates survive long-term). Target volume = **100k–1M/day** → default sampling keeps **non-continue outcomes 100%** and samples the delivered/happy path at **0.1**, with a `TRACE_MAX_ROWS` hard ceiling as backstop (small deployments can set sample rate to 1.0).

## 1. Current-state findings
- **Inbound flow:** SMTP gateway (separate process) → `apiclient.DeliverMessage` → API `deliverToLocal` (`messages.go:1697`) → `engine.Execute` (`messages.go:1814`) → terminal switch (`messages.go:1821`): reject/quarantine/discard/defer/continue. **Message row created ONLY on continue (`messages.go:1892`)** → rejected/quarantined mail has no Message row (trace must key on its own id / `rfc_message_id`).
- **`DeliverRequest` carries no TLS/transport field** in this checkout (the #70 `received_tls` is not present here); `Envelope.TLS` (`types.go:54`) exists but is never populated inbound. Transport plumbing is net-new (PR2).
- **Single seam:** `Engine.Execute` (`engine.go:50`) builds a `StepResult{FilterName,FilterType,Action,Skipped,Log,Duration,Error}` per filter (`engine.go:102`) → `ExecutionResult{FinalAction,Steps,RejectMsg,Duration}`. Auth stages (SPF/DKIM/DMARC/ARC) ARE filters → covered by the same seam.
- **Prometheus defined-but-DEAD:** `internal/metrics/metrics.go` registers a full set; only HTTP metrics are live (`middleware.go:49`). `MessagesReceived`, `MessagesSent`, `PipelineFilterDuration{filter}`, `AuthFailures{protocol}`, `Queue*` etc. have ZERO call sites → export constant 0. Wire these up. `normalisePath` (`middleware.go:71`) sets the bounded-cardinality convention; `CertExpiryDays{domain}` violates it (unbounded label — fix).
- **`PipelineLog`** (`models/pipeline.go:42`): synchronous hot-path write (`messages.go:1342`), `MessageID` always nil, no sampling, no pruning (unbounded), not aggregatable. Evolve into `MessageTrace`.
- **Reasons are free text:** `FilterLog.Detail` embeds domains/timestamps (`dmarc_check.go:176`, `greylist.go:90`); no numeric score field. Free text must NEVER become a metric label.

## 2. Capture seam (instrument once)
Inject a no-op `Observer` interface into `NewEngine` (keeps `internal/metrics` a leaf, engine testable). In `Execute` at step finalize (`engine.go:102`), per filter: observe filter duration + `stage_decision{filter,action}` + (if auth filter) `auth_verdict{mechanism,result}`. At terminal sites (`messages.go:1821`, `restmail.go:242`, queue worker): `terminal{direction,outcome}`, `reject_reason{reason_code}`, `messages_received{transport}` (PR2), `messages_sent{transport}`. All lock-free atomics → safe inline on hot path. Trace write is async (§5).

## 3. Cardinality discipline (bounded enum labels only)
| Metric | Labels | Bounded domain |
|---|---|---|
| `restmail_messages_received_total` | `transport` | tls,plaintext |
| `restmail_messages_sent_total` | `transport` | tls,plaintext |
| `restmail_pipeline_stage_decisions_total` | `filter`,`action` | registry names + `custom`; continue/reject/quarantine/discard/defer/skipped/error |
| `restmail_pipeline_filter_duration_seconds` (hist) | `filter` | bounded filter set |
| `restmail_pipeline_terminal_total` | `direction`,`outcome` | inbound/outbound; delivered/queued/rejected/quarantined/discarded/deferred |
| `restmail_auth_verdict_total` | `mechanism`,`result` | spf/dkim/dmarc/arc; pass/fail/none/neutral/softfail/temperror/permerror |
| `restmail_pipeline_reject_reason_total` | `reason_code` | fixed enum (below) |
| `restmail_trace_dropped_total` | — | async backpressure |

`reason_code` enum, mapped once from `(filter,action,Result)`: `dmarc_reject, dmarc_quarantine, spf_fail, greylist_defer, size_exceeded, rate_limited, virus_detected, spam_threshold, recipient_unknown, header_invalid, custom_reject, other`. **Trace-only (never a label):** sender, recipient, from-domain, message-id, client IP, free-text detail, spam score. Custom filter names → `"custom"`. Total series ≈ low hundreds, volume-independent.

## 4. Per-message trace model (single row + JSON stages)
Do NOT normalize one-row-per-stage (Prometheus already serves `GROUP BY stage`; normalized = ~12× write amplification for a query pattern we don't need on the DB). Evolve `PipelineLog` → `MessageTrace`:
```
id, message_id *uint (FK, set only when delivered), rfc_message_id string idx,
direction, transport, mail_from, rcpt_to, client_ip (trace-only PII),
pipeline_id, final_action, outcome idx, reason_code idx, spam_score *float32,
duration_ms, stages jsonb ([]StepResult), sampled bool,
created_at idx, expires_at idx
```
Indexes: rfc_message_id, message_id, created_at (prune), (outcome,created_at).

## 5. Async, non-blocking capture
`TraceRecorder`: buffered channel (~4096) → one goroutine batch-inserts (`CreateInBatches`) on a ticker (reuse `worker.go:164` pattern). Sampling gate before enqueue. **Drop-on-full → `trace_dropped_total`.inc(), never block.** Degradation: DB slow → drop traces, mail unaffected, aggregates still exact (counted inline). Replaces synchronous `logPipelineExecution`/`restmail.go:234`.

## 6. Retention + rollup (self-contained)
- **Storage estimate:** ~3 KB/trace (12-stage JSON + PII cols). 10k/day→~210 MB/7d; 100k/day→~2.1 GB/7d; 1M/day→~21 GB/7d. ⇒ traces MUST be bounded.
- **Rollup worker** (background, watermark-based, idempotent — mirror DMARC Reporter / queue worker): periodically aggregate raw traces → condensed rollup tables (time-bucketed counters over the bounded dims + duration percentiles). **INVARIANT: never prune a raw trace before it's rolled up** (prune = older-than-window AND past watermark). Consider multi-resolution (fine recent, daily older).
- **Dials (volume/cost, not security):** `TRACE_RETENTION_DAYS`=7; `TRACE_SAMPLE_RATE`=**0.1** (happy path) with **always-keep non-continue outcomes 100%** (targets the 100k–1M/day scale ⇒ ~2 GB/7d vs ~21 GB unsampled); `TRACE_MAX_ROWS` hard ceiling backstop. Hourly pruner. Small deployments set rate=1.0.
- Aggregate rollups long-retention; in-app dashboard reads rollups (true windowed history without Grafana).

## 7. Analytics surface
- Aggregate funnel from rollups (received→auth→stage decisions→terminal) + top reasons + per-filter rates + windows. `GET /api/v1/admin/pipelines/analytics` gated by new capability; also a `prometheus.Gatherer` snapshot endpoint for since-start totals. Extend `stats.go`.
- Per-message trace: evolve `ListPipelineLogs` (adds rfc_message_id/outcome/reason_code filters) + `GET /api/v1/admin/messages/{id}/trace`. Admin message-detail stage timeline.
- Frontend: `admin/` funnel widget + trace timeline — **flag: admin has no CI** (keep minimal; prefer Grafana for rich charts).

## 8. PR slices
1. **PR1 — aggregate metrics wiring** (hot path, no schema): Observer seam in `Engine.Execute`; wire `stage_decisions{filter,action}` + `filter_duration{filter}` + `auth_verdict{mechanism,result}` + `terminal{direction,outcome}` + `messages_sent`; filter-name allowlist→`custom`; fix `CertExpiryDays` label. Atomics only. Scrapeable on merge. (reason_code + messages_received{transport} deferred to PR2 so label sets are right from the start.)
2. **PR2 — reason-code taxonomy + transport**: bounded `reason_code` enum + `(filter,action,result)→code` mapper (used by metric + trace); add `transport` through `DeliverRequest`+`Envelope`; wire `reject_reason` + `messages_received{transport}`.
3. **PR3 — MessageTrace schema + async recorder** (HARDEST TO CHANGE): migration `PipelineLog`→`MessageTrace`; `TraceRecorder`; populate `message_id` on delivered; replace synchronous writes.
4. **PR4 — rollup worker + retention**: rollup tables + watermark rollup worker; `TRACE_RETENTION_DAYS`/`SAMPLE_RATE`/`MAX_ROWS`; pruner (roll-up-before-prune invariant).
5. **PR5 — analytics + trace read API** (reads rollups): funnel + trace endpoints + RBAC capability.
6. **PR6 — admin frontend** (unguarded): funnel widget + trace timeline, minimal/additive.

## 9. Decisions (all resolved 2026-07-23)
- Consumption = self-contained DB rollups + optional Prometheus. Custom filter → `"custom"`. 7-day raw window. Dedicated `CapTracesRead`/`CapStatsRead` capability.
- PII = store raw, pruned at the 7-day window.
- Volume target = 100k–1M/day → `TRACE_SAMPLE_RATE`=0.1 happy path, anomalies 100%, `TRACE_MAX_ROWS` backstop.
No open questions remain; PR2–PR6 are fully specified.

## Amendments during build
- **PR1 merged (#72).** Inbound pipeline metrics live in the API process `/metrics`. **Follow-up:** the queue worker (outbound) runs in the smtp-gateway process, which has NO `/metrics` endpoint — `messages_sent`/`terminal{outbound}` are wired but won't surface until the gateway processes expose `/metrics` (multi-process scraping). Backlog.
- **PR2 merged (#73), PR3 merged (#74).** MessageTrace table + async recorder live.
- **PR4 accuracy refinement:** rollups are computed by SNAPSHOTTING the 100%-accurate Prometheus counters into time-bucketed DB rows — NOT by re-aggregating the sampled traces (which would undercount delivered mail ~10×). Sampling affects only per-message forensic detail; aggregate counts stay exact. Pruning traces never loses aggregate history (invariant structurally satisfied).
- **#70 already added TLS capture** (`ReceivedTLS *bool` + `TLSVersion` on `DeliverRequest`, `received_tls` on the message). PR2 REUSES it — do not re-plumb transport; just populate `Envelope.TLS` from it + wire the transport-labeled received metric.
