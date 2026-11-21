# Local release-image evidence

This is the local proof for #2104 AC02 and the inherited #2118 AC04. It builds
production runtime images, publishes nothing, and changes no release workflow.
The installed-cluster promotion gate remains `scripts/release_gate.py`; this
recipe does not claim AC05, AC06, public ACME acceptance, or a release promotion.

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

**Digest boundary:** local-only runs use Docker's image configuration digest
(`docker image inspect .Id`). It binds the configuration and root filesystem
layer content; both Trivy reports carry this exact ImageID. It is not a registry
index/manifest digest, and must not be passed off as one in the AC06 cluster
promotion receipts. Publishing/index attestation and workflow wiring remain
separate work. Both release platforms can be collected and exercised, each in
its own directory; emulated runtime checks are not native-host performance proof.

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
never CA keys. It exercises TLS reload, then the existing real eight-operation
fresh-start conformance baseline, saving the actual Vertrag reports and all
container logs. Containers, anonymous volumes, temporary keys and the owned
network are removed on exit. Built images and the scan cache remain for explicit
owned-resource cleanup after inspection.

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
python3 -m unittest discover -s scripts/tests -p 'test_tls*reload*.py'
```

Run receipts and findings belong on issue #2104, including the full tested
commit, per-platform digests, commands, counts and policy outcome. Local files
alone are not a durable CI or published-release attestation.
