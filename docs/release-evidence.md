# Release-image evidence

The Docker workflow collects evidence for #2104 AC02 and the inherited AC04
and uses it to gate versioned image publication. The same checks can run locally
without publishing anything. This does not run or replace the installed-cluster
stages, the external fixture TLS topology, or public ACME acceptance.

## Certificate reload checks

The named Go checks run real listeners in-process:

- `TestIMAPListenersServeRenewedCertificate`: implicit TLS and STARTTLS.
- `TestPOP3ListenersServeRenewedCertificate`: implicit TLS and STLS.
- `TestSMTPListenersServeRenewedCertificate`: submission TLS and inbound STARTTLS.
- `TestAPIHTTPSListenerAndGatewayClientServeRenewedCertificates`: real HTTPS
  over the production internal-mTLS configuration, with both server and gateway
  client identity replacement.

Each verifies the renewed leaf, hostname and trusted chain. Existing connections
continue protocol/HTTP exchanges. Missing and invalid replacement are separate
controls: fresh handshakes still present the last usable certificate, the reload
failure counter and failing gauge rise, and repair clears the gauge.

```sh
go test -count=1 -timeout=2h ./internal/gateway/imap ./internal/gateway/pop3 \
  ./internal/gateway/smtp ./internal/mtls \
  -run 'ServeRenewedCertificate|TestAPIHTTPSListenerAndGatewayClientServeRenewedCertificates'
```

The Go API's public port is HTTP, behind a TLS terminator. The local topology's
HTTPS terminator is the nginx stream front started by `tasks/smtp-gateway.yml`.
`tasks/tls-front-reload.sh` fingerprints its certificate and key every five
seconds, validates a replacement with `nginx -t`, and gracefully signals only its
own nginx master. Missing or invalid material is reported without signalling
the master. Existing active work drains; nginx may close idle HTTP keep-alives
on a graceful reload. The live probe therefore requires established sessions
to survive on the Go listeners, not idle front-proxy HTTP connections.
`TLSFrontReloadTests` independently tests valid, missing and invalid publication.

The release-image disposable install below verifies all six listeners the
shipped commands actually enable: API HTTPS front, API internal mTLS, IMAP TLS,
POP3 TLS, SMTP submission TLS and SMTP STARTTLS. Each has five observed phases:
renewed, missing, repaired, invalid and recovered. Client IMAP 143 and POP3 110
are deliberately not enabled by the shipped commands; their library STARTTLS
paths remain covered by the Go checks, not invented as deployed listeners.
For missing and invalid material the probe requires a new failure report from
each affected service. It repeats verified handshakes while waiting up to two
more five-second reload intervals for those reports.
CA trust pools do not reload: these checks retain each original issuer and
replace leaves. Trust-anchor rotation still requires coordinated restart.

For the existing owned private topology, the strict independent probe is:

```sh
chore topology:up TOPOLOGY=proof2104
chore topology:tls-reload TOPOLOGY=proof2104
chore topology:down TOPOLOGY=proof2104
```

The probe checks ownership labels, actual service IPs and configured paths,
uses a digest-pinned Python/OpenSSL helper, restores the original pairs even
on failure, and refuses service restarts or unreported failures. It never
disables certificate verification. Fixture-specific failures are tracked in
issue #2104, not converted into a passing or skipped topology proof.

## Vulnerability policy

Policy ID: `restmail-fixable-high-critical-v1`.

- Every published component in `.github/workflows/docker.yml` needs evidence.
  Build from the pinned Dockerfile, committed source and dependency lockfiles;
  a dirty checkout is refused. Keep build logs and the full source commit.
- Use the same digest-pinned Trivy version as the release workflow. Save a
  complete JSON scan and a nonempty CycloneDX SBOM for each built image. Do not
  prefilter severities, unfixed findings or ignored entries from the report.
- A fixable HIGH or CRITICAL vulnerability refuses the image, matching the
  existing Docker workflow's vulnerability threshold. HIGH/CRITICAL secret
  findings also refuse it. Lower-severity and unfixed findings remain visible
  in the complete scan and evaluation; they are not silently removed.
- An exception applies only when `.trivyignore` contains the exact identifier
  with an applicability/fix disposition and tracking issue. Its current content
  hash is part of every evaluation. This change adds no exception.
- Recompute the policy from the raw report when checking. A stored `pass` cannot
  override findings, a missing evaluation, or a changed ignore policy.
- Scan, SBOM, build and running-container identities must all match. Record
  SHA-256 hashes of the scan, SBOM, served description and JUnit bytes.
- Fresh-start evidence must have successful exit status, nonzero exchanges,
  zero failures/errors/skips and the complete public read-only operation
  selection used by `cluster_e2e.Stages.contract_examples`. A new failed run
  invalidates any previous successful receipt.

**Digest boundary:** both Trivy reports and the running containers identify
Docker's image configuration digest (`docker image inspect .Id`). CI also saves
the exact registry index and native platform manifest bytes. The promotion gate
hashes those bytes to bind the tested configuration to the build's index digest;
it never substitutes the configuration digest for the index digest.

## CI receipt and image promotion

`Docker Build & Push` runs on its existing main and version-tag pushes, never
on pull requests. Each matrix leg uploads `release-digest-<attempt>-<image>`
before running the existing fixable HIGH/CRITICAL gates on amd64 and arm64.
Tag builds push only by digest at this point, not under the SemVer name. Main
keeps its existing development tags.

The `Release image evidence` job downloads those seven digests in the same run
and attempt. `Collect scans and CycloneDX SBOMs from the built images` pulls the
native amd64 configurations sequentially; it does not rebuild them. `Probe TLS
renewal and the fresh-start baseline` starts one isolated topology and records
eight required public GET operations and the six live TLS listeners. The two
runtime checks and the scan policy are independent: a finding does not erase the
runtime diagnostics. The job has bounded step times, tears down its own labeled
containers, network and volumes, and uploads `release-image-receipt-<attempt>`
even on failure if evidence exists. The receipt contains:

- `builds.json`, the seven `*.index.json` and `*.manifest.json` digest links;
- seven each of `*.scan.json`, `*.sbom.json` and `*.json` policy evaluations,
  plus scanner logs;
- `fresh-start.json`, the served OpenAPI, Vertrag JUnit/HTML/logs under
  `contract/`, `tls-reload.json` with observed original/replacement fingerprints,
  and disposable topology container logs. Private keys are not uploaded.

`Gate and promote release images` downloads the receipt and the separate build
digests. Both upstream jobs must succeed. `release_gate.py --evidence-directory`
refuses missing, failed, stale or mismatched evidence before any version tag is
written. It checks the full commit, exact seven registry indices and the scan,
fresh-start and reload receipts (maximum age 26 hours). It then copies each
authorized multi-platform index to its version name without rebuilding. The
seven registry writes are not an atomic transaction; a registry outage can
interrupt publication. If rerunning, rerun **all** jobs so every input has the
same run attempt. The separate GitHub CLI release and Helm workflow are unchanged.
The stronger installed-cluster `--receipts` gate still requires all installed
stages (AC05); this image receipt is not a replacement for those receipts.

`ReleasePromotionWorkflowTests` executes the actual workflow promotion step with
a recorder instead of writing to a registry. It proves refusal with missing,
stale, failed, tampered and wrong-digest input, and that removing the gate lets
seven writes through despite a missing SBOM. It also verifies the successful
command names the authorized digests and checks that the build cannot publish
SemVer before both prerequisite jobs pass.

## Reproduce the evidence

Prerequisites: Docker/BuildKit, Go, OpenSSL, Python with `pyyaml` and
`jsonschema[format]`, and room for the image builds. Vertrag is installed through
the existing checksummed `scripts/vertrag_pin.py` cache. Check `df -h` first.

Commit the implementation before collecting evidence. For each release platform,
use a unique tag prefix and project name. For example, from a clean checkout:

```sh
python3 scripts/release_evidence.py collect \
  --platform linux/arm64 --directory /tmp/release-proof/arm64 \
  --tag-prefix restmail-proof-arm64/ --label restmail-release-proof=arm64
python3 scripts/release_fresh_start.py \
  --directory /tmp/release-proof/arm64 --project restmail-proof-arm64
```

Repeat with `linux/amd64`, a separate directory, prefix and project. Collection
continues through all seven images even when a build or scan fails, and returns
nonzero for policy findings or missing evidence. Do not ignore that result.
Fresh-start can still run on policy-refused images to obtain the independent
runtime evidence; its success does not authorize those images.

The runner creates an owned private Docker bridge, a new tmpfs-backed
PostgreSQL 16 database and all seven exact images. Only random loopback ports
are published. Its generated secrets and separate public/internal CAs belong
only to that install; services receive only their selected leaf material,
never CA keys. It exercises the real eight-operation fresh-start baseline and
TLS reload, saving the actual Vertrag reports, observed handshakes and all
container logs. Containers, anonymous volumes, temporary keys and the owned
network are removed on exit. Built images and the scan cache remain for explicit
owned-resource cleanup after inspection.

On Docker Desktop for Mac, if its `/var/folders` file share does not propagate
atomic certificate replacements, add `TMPDIR=/private/tmp` to the
`release_fresh_start.py` command. The Linux CI topology uses native bind mounts.

Check against the image identities Docker actually holds, not an unverified
claim copied from a scan. Example in Bash:

```bash
images=()
for component in api smtp-gateway imap-gateway pop3-gateway js-filter webmail admin; do
  digest=$(docker image inspect --format '{{.Id}}' "restmail-proof-arm64/$component:arm64")
  images+=(--image "$component=$digest")
done
python3 scripts/release_evidence.py check \
  --directory /tmp/release-proof/arm64 --commit "$(git rev-parse HEAD)" "${images[@]}"
```

`check` is offline and needs no Docker daemon. A refusal is the correct result
for policy findings, absent/changed reports, another digest, incomplete images,
or missing/failed fresh-start evidence. `ReleaseEvidenceTests` includes named
controls for each; `ReleaseFreshStartTests` protects loopback-only publication
and refuses to remove a network whose creation failed.

```sh
python3 -m unittest discover -s scripts/tests -p 'test_release_evidence.py'
python3 -m unittest discover -s scripts/tests -p 'test_release_fresh_start.py'
python3 -m unittest discover -s scripts/tests -p 'test_release_promotion_workflow.py'
python3 -m unittest discover -s scripts/tests -p 'test_tls*reload*.py'
```

Run receipts and findings belong on issue #2104, including the full tested
commit, per-platform digests, commands, counts and policy outcome. Local files
alone are not a durable CI or published-release attestation.
