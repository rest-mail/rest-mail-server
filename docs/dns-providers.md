# DNS Provider System

rest-mail uses a pluggable DNS provider system to manage the DNS records required for mail delivery (A, MX, SPF, DMARC, DKIM). The system is defined in `internal/dns/` and allows different backends depending on whether you are running in local development, Kubernetes, or a manually managed environment.

## Overview

When rest-mail manages a mail domain, it needs to ensure the correct DNS records exist: an A record pointing to the mail server, an MX record for inbound routing, SPF and DMARC TXT records for authentication. Rather than hardcoding a single DNS backend, rest-mail defines a `Provider` interface and ships three built-in adapters. A factory function selects the adapter at startup based on the `DNS_PROVIDER` environment variable.

```
                +-------------------+
                |  dns.Provider     |  (interface)
                +-------------------+
                        |
          +-------------+-------------+
          |             |             |
    ManualProvider  DnsmasqProvider  ExternalDNSProvider
    (log only)     (file-based)     (K8s CRD YAML)
```

Source files:

| File | Purpose |
|------|---------|
| `internal/dns/provider.go` | Interface definition, shared types, `RequiredRecords` helper |
| `internal/dns/manual.go` | Manual (log-only) adapter |
| `internal/dns/dnsmasq.go` | dnsmasq file-based adapter for local development |
| `internal/dns/externaldns.go` | Kubernetes external-dns CRD adapter |
| `internal/dns/factory.go` | Factory function that creates a provider by name |

---

## The Provider Interface

```go
// Provider is the interface for pluggable DNS providers.
type Provider interface {
    // EnsureRecords creates or updates DNS records for a domain.
    EnsureRecords(ctx context.Context, domain string, records []DNSRecord) error

    // RemoveRecords removes all DNS records for a domain.
    RemoveRecords(ctx context.Context, domain string) error

    // VerifyRecords checks that DNS records are propagated correctly.
    VerifyRecords(ctx context.Context, domain string) ([]VerifyResult, error)
}
```

Every provider must implement all three methods:

- **EnsureRecords** -- Idempotent. Called when a domain is created or its records need updating. The provider should create records that do not exist and update records that have changed.
- **RemoveRecords** -- Called when a domain is deleted. The provider should clean up all records it previously created for that domain.
- **VerifyRecords** -- Called to check whether records have propagated. Returns a per-record result indicating whether the record was found and what the actual value is.

---

## Shared Types

### DNSRecord

Represents a single DNS record to create or update.

```go
type DNSRecord struct {
    Type     string `json:"type"`     // A, MX, TXT, PTR, CNAME
    Name     string `json:"name"`     // e.g. "mail1.test", "_dmarc.mail1.test"
    Value    string `json:"value"`
    TTL      int    `json:"ttl"`
    Priority int    `json:"priority"` // for MX records
}
```

### VerifyResult

Represents the verification result for a single record.

```go
type VerifyResult struct {
    Record   DNSRecord `json:"record"`
    Found    bool      `json:"found"`
    Actual   string    `json:"actual,omitempty"`
    Error    string    `json:"error,omitempty"`
}
```

### RequiredRecords Helper

The `RequiredRecords` function generates the standard set of DNS records every mail domain needs:

```go
func RequiredRecords(domain, ip string) []DNSRecord {
    return []DNSRecord{
        {Type: "A", Name: domain, Value: ip, TTL: 3600},
        {Type: "MX", Name: domain, Value: domain, TTL: 3600, Priority: 10},
        {Type: "TXT", Name: domain, Value: "v=spf1 ip4:" + ip + " -all", TTL: 3600},
        {Type: "TXT", Name: "_dmarc." + domain,
         Value: "v=DMARC1; p=reject; rua=mailto:postmaster@" + domain, TTL: 3600},
    }
}
```

This produces four records:

| Type | Name | Value |
|------|------|-------|
| A | `example.com` | `1.2.3.4` |
| MX | `example.com` | `example.com` (priority 10) |
| TXT | `example.com` | `v=spf1 ip4:1.2.3.4 -all` |
| TXT | `_dmarc.example.com` | `v=DMARC1; p=reject; rua=mailto:postmaster@example.com` |

DKIM records are not included here because they are generated separately by the `certgen` tool and require the public key material.

---

## Built-in Providers

### 1. Manual Provider

**File:** `internal/dns/manual.go`
**Name:** `"manual"`

The simplest possible adapter. It does not create, modify, or verify any DNS records. Instead, it logs the records that need to be created manually by the operator.

**Use case:** Production environments where DNS is managed externally (e.g., through a registrar's control panel, Terraform, or a DNS provider with no API). The operator reads the log output and creates records by hand.

**Behavior:**

| Method | Action |
|--------|--------|
| `EnsureRecords` | Logs each required record at INFO level |
| `RemoveRecords` | Logs that records need manual removal |
| `VerifyRecords` | Returns `nil, nil` (verification not supported) |

**Example log output:**

```
{"level":"INFO","msg":"manual: DNS records need to be created manually","domain":"example.com","count":4}
{"level":"INFO","msg":"manual: required record","type":"A","name":"example.com","value":"1.2.3.4","priority":0}
{"level":"INFO","msg":"manual: required record","type":"MX","name":"example.com","value":"example.com","priority":10}
```

### 2. Dnsmasq Provider

**File:** `internal/dns/dnsmasq.go`
**Name:** `"dnsmasq"`
**Default provider** (used when `DNS_PROVIDER` is not set)

Writes DNS records directly to a dnsmasq configuration file. This is the adapter used in the Docker Compose development environment, where a dnsmasq container provides DNS resolution for the `.test` domains.

**Use case:** Local development with Docker Compose. The dnsmasq container reads config files from a mounted volume, so writing a file is equivalent to creating DNS records.

**Constructor:**

```go
func NewDnsmasqProvider(configPath string) *DnsmasqProvider
```

If `configPath` is empty, it defaults to `/etc/dnsmasq.d/domains.conf`.

**Behavior:**

| Method | Action |
|--------|--------|
| `EnsureRecords` | Writes a dnsmasq config file with `address=`, `mx-host=`, `txt-record=`, `ptr-record=` directives |
| `RemoveRecords` | Deletes the config file |
| `VerifyRecords` | Returns `nil, nil` (assumes records are always correct in dev) |

**Example generated config:**

```
# DNS records for mail1.test
address=/mail1.test/172.20.0.11
mx-host=mail1.test,mail1.test,10
txt-record=mail1.test,"v=spf1 ip4:172.20.0.11 -all"
txt-record=_dmarc.mail1.test,"v=DMARC1; p=reject; rua=mailto:postmaster@mail1.test"
```

**Supported record types:**

| DNS Type | dnsmasq Directive |
|----------|-------------------|
| A | `address=/<name>/<value>` |
| MX | `mx-host=<name>,<value>,<priority>` |
| TXT | `txt-record=<name>,"<value>"` |
| PTR | `ptr-record=<name>,<value>` |

CNAME records are logged as unsupported warnings since dnsmasq handles them differently.

### 3. ExternalDNS Provider

**File:** `internal/dns/externaldns.go`
**Name:** `"externaldns"`

Generates Kubernetes `DNSEndpoint` Custom Resource Definition (CRD) YAML files that the [external-dns](https://github.com/kubernetes-sigs/external-dns) controller watches and reconciles against a real DNS provider (Route53, CloudFlare, Google Cloud DNS, etc.).

**Use case:** Kubernetes deployments where external-dns is already managing DNS. rest-mail writes CRD manifests to a shared volume; external-dns picks them up and creates the actual DNS records.

**Constructor:**

```go
func NewExternalDNSProvider(outputDir string) *ExternalDNSProvider
```

If `outputDir` is empty, it defaults to `/etc/externaldns/`.

**Behavior:**

| Method | Action |
|--------|--------|
| `EnsureRecords` | Writes a `DNSEndpoint` YAML file to `<outputDir>/restmail-<domain>.yaml` |
| `RemoveRecords` | Deletes the YAML file |
| `VerifyRecords` | Performs live DNS lookups (A, MX, TXT, DMARC) and returns per-record results |

**Example generated YAML** (for domain `example.com`):

```yaml
apiVersion: externaldns.k8s.io/v1alpha1
kind: DNSEndpoint
metadata:
  name: restmail-example-com
  labels:
    app: restmail
    domain: example.com
spec:
  endpoints:
    - dnsName: example.com
      recordType: A
      targets:
        - "1.2.3.4"
      recordTTL: 3600
    - dnsName: example.com
      recordType: MX
      targets:
        - "10 example.com"
      recordTTL: 3600
    - dnsName: example.com
      recordType: TXT
      targets:
        - "v=spf1 ip4:1.2.3.4 -all"
      recordTTL: 3600
    - dnsName: _dmarc.example.com
      recordType: TXT
      targets:
        - "v=DMARC1; p=reject; rua=mailto:postmaster@example.com"
      recordTTL: 3600
```

The `VerifyRecords` method on this provider is the only built-in adapter that performs actual DNS lookups. It checks:
- A records via `net.LookupHost`
- MX records via `net.LookupMX`
- TXT records (SPF) via `net.LookupTXT` on the domain
- TXT records (DMARC) via `net.LookupTXT` on `_dmarc.<domain>`

---

## Configuration

The DNS provider is selected via the `DNS_PROVIDER` environment variable:

| Value | Provider | Default Options |
|-------|----------|-----------------|
| `dnsmasq` (default) | DnsmasqProvider | Config path: `/etc/dnsmasq.d/domains.conf` |
| `manual` | ManualProvider | None |
| `externaldns` | ExternalDNSProvider | Output dir: `/etc/externaldns/` |

The default value is `"dnsmasq"`, which is appropriate for the Docker Compose development environment.

Provider-specific options are passed as additional arguments to the factory function (see below), not through environment variables. The config struct in `internal/config/config.go` only stores the provider name:

```go
type Config struct {
    // ...
    DNSProvider string  // populated from DNS_PROVIDER env var
    // ...
}
```

---

## Factory Function

The `NewProvider` factory creates a provider instance by name:

```go
func NewProvider(name string, opts ...string) (Provider, error)
```

**Parameters:**

| Parameter | Description |
|-----------|-------------|
| `name` | Provider name: `"manual"`, `"dnsmasq"`, or `"externaldns"` |
| `opts[0]` | Optional. For `dnsmasq`: config file path. For `externaldns`: output directory. |

**Usage examples:**

```go
// Default dnsmasq provider
p, err := dns.NewProvider("dnsmasq")

// Dnsmasq with custom config path
p, err := dns.NewProvider("dnsmasq", "/custom/path/domains.conf")

// Manual provider (no options)
p, err := dns.NewProvider("manual")

// ExternalDNS with custom output directory
p, err := dns.NewProvider("externaldns", "/var/run/externaldns/")

// Using the value from config
p, err := dns.NewProvider(cfg.DNSProvider)
```

An unrecognized name returns an error:

```
unknown DNS provider: "bogus" (supported: manual, dnsmasq, externaldns)
```

---

## Integration Points

The DNS provider is wired into the system at several levels:

### 1. API Router

The `NewRouter` function in `internal/api/routes.go` accepts a `dns.Provider` parameter:

```go
func NewRouter(db *gorm.DB, jwtService *auth.JWTService, cfg *config.Config,
    dnsProvider dns.Provider, acmeClient ...*acmeclient.Client) http.Handler
```

This provider is passed to the `DomainHandler`:

```go
// internal/api/handlers/domains.go
type DomainHandler struct {
    db  *gorm.DB
    dns dns.Provider
}

func NewDomainHandler(db *gorm.DB, dnsProvider dns.Provider) *DomainHandler {
    return &DomainHandler{db: db, dns: dnsProvider}
}
```

### 2. Domain Handler DNS Check Endpoint

The `DomainHandler.DNSCheck` method (`GET /api/v1/admin/domains/:id/dns`) performs live DNS verification for a domain. It checks MX, SPF, DMARC, DKIM, MTA-STS, and TLS-RPT records using Go's `net` package directly, independent of the provider. This endpoint is useful for confirming that records have propagated regardless of which provider created them.

### 3. API Server Startup

In `cmd/api/main.go`, the DNS provider is created from the config and passed to the router. The typical initialization flow:

```go
cfg, _ := config.Load()
dnsProvider, _ := dns.NewProvider(cfg.DNSProvider)
router := api.NewRouter(database, jwtService, cfg, dnsProvider)
```

### 4. RequiredRecords in Domain Lifecycle

When a new mail domain is added, the system calls `dns.RequiredRecords(domain, ip)` to generate the standard record set, then passes it to the provider's `EnsureRecords` method. When a domain is removed, `RemoveRecords` is called to clean up.

---

## Writing a Custom Adapter

To add support for a new DNS backend (e.g., Cloudflare, Route53, Google Cloud DNS), follow these steps:

### Step 1: Create the Provider File

Create a new file in `internal/dns/`. The file should define a struct that implements the `Provider` interface.

```go
// internal/dns/cloudflare.go
package dns

import (
    "context"
    "fmt"
    "log/slog"
    "net/http"
    "strings"
)

// CloudflareProvider implements Provider using the Cloudflare API.
type CloudflareProvider struct {
    apiToken string
    zoneIDs  map[string]string // domain -> zone ID cache
    client   *http.Client
}

// NewCloudflareProvider creates a new Cloudflare DNS provider.
// apiToken is a Cloudflare API token with DNS edit permissions.
func NewCloudflareProvider(apiToken string) *CloudflareProvider {
    return &CloudflareProvider{
        apiToken: apiToken,
        zoneIDs:  make(map[string]string),
        client:   &http.Client{},
    }
}
```

### Step 2: Implement EnsureRecords

This method should be idempotent: create records that are missing and update records that differ.

```go
func (p *CloudflareProvider) EnsureRecords(ctx context.Context, domain string, records []DNSRecord) error {
    slog.Info("cloudflare: ensuring DNS records", "domain", domain, "count", len(records))

    zoneID, err := p.getZoneID(ctx, domain)
    if err != nil {
        return fmt.Errorf("cloudflare: failed to get zone ID for %s: %w", domain, err)
    }

    for _, r := range records {
        // Fetch existing records of this type+name from the Cloudflare API.
        // If found, update. If not found, create.
        existing, err := p.findRecord(ctx, zoneID, r.Type, r.Name)
        if err != nil {
            return fmt.Errorf("cloudflare: lookup failed for %s %s: %w", r.Type, r.Name, err)
        }

        if existing != nil {
            err = p.updateRecord(ctx, zoneID, existing.ID, r)
        } else {
            err = p.createRecord(ctx, zoneID, r)
        }
        if err != nil {
            return fmt.Errorf("cloudflare: failed to upsert %s %s: %w", r.Type, r.Name, err)
        }
    }

    return nil
}
```

### Step 3: Implement RemoveRecords

```go
func (p *CloudflareProvider) RemoveRecords(ctx context.Context, domain string) error {
    slog.Info("cloudflare: removing DNS records", "domain", domain)

    zoneID, err := p.getZoneID(ctx, domain)
    if err != nil {
        return fmt.Errorf("cloudflare: failed to get zone ID for %s: %w", domain, err)
    }

    // List all records tagged with restmail, then delete each one.
    records, err := p.listRecords(ctx, zoneID, domain)
    if err != nil {
        return err
    }
    for _, rec := range records {
        if err := p.deleteRecord(ctx, zoneID, rec.ID); err != nil {
            return err
        }
    }
    return nil
}
```

### Step 4: Implement VerifyRecords

Use live DNS lookups (similar to the ExternalDNS adapter) or the Cloudflare API to verify records exist.

```go
func (p *CloudflareProvider) VerifyRecords(ctx context.Context, domain string) ([]VerifyResult, error) {
    slog.Info("cloudflare: verifying DNS records", "domain", domain)

    var results []VerifyResult

    // Verify A record
    addrs, err := net.LookupHost(domain)
    if err != nil {
        results = append(results, VerifyResult{
            Record: DNSRecord{Type: "A", Name: domain},
            Found:  false,
            Error:  err.Error(),
        })
    } else {
        results = append(results, VerifyResult{
            Record: DNSRecord{Type: "A", Name: domain},
            Found:  true,
            Actual: strings.Join(addrs, ", "),
        })
    }

    // ... repeat for MX, TXT (SPF), TXT (DMARC) ...

    return results, nil
}
```

### Step 5: Register in the Factory

Add a new case to the `NewProvider` function in `internal/dns/factory.go`:

```go
func NewProvider(name string, opts ...string) (Provider, error) {
    switch name {
    case "manual":
        return NewManualProvider(), nil

    case "dnsmasq":
        var configPath string
        if len(opts) > 0 {
            configPath = opts[0]
        }
        return NewDnsmasqProvider(configPath), nil

    case "externaldns":
        var outputDir string
        if len(opts) > 0 {
            outputDir = opts[0]
        }
        return NewExternalDNSProvider(outputDir), nil

    // Add the new provider:
    case "cloudflare":
        var apiToken string
        if len(opts) > 0 {
            apiToken = opts[0]
        }
        if apiToken == "" {
            return nil, fmt.Errorf("cloudflare provider requires an API token (pass as opts[0])")
        }
        return NewCloudflareProvider(apiToken), nil

    default:
        return nil, fmt.Errorf("unknown DNS provider: %q (supported: manual, dnsmasq, externaldns, cloudflare)", name)
    }
}
```

### Step 6: Use It

Set the environment variable and restart the API server:

```bash
export DNS_PROVIDER=cloudflare
```

If the provider needs credentials or other options beyond what the factory's variadic `opts` provide, consider adding dedicated environment variables (e.g., `CLOUDFLARE_API_TOKEN`) and reading them in `config.Load()`.

---

## Testing

Providers are straightforward to test because the interface is small. For unit tests, create a mock:

```go
type mockProvider struct {
    ensureCalled bool
    removeCalled bool
    verifyCalled bool
}

func (m *mockProvider) EnsureRecords(ctx context.Context, domain string, records []DNSRecord) error {
    m.ensureCalled = true
    return nil
}

func (m *mockProvider) RemoveRecords(ctx context.Context, domain string) error {
    m.removeCalled = true
    return nil
}

func (m *mockProvider) VerifyRecords(ctx context.Context, domain string) ([]VerifyResult, error) {
    m.verifyCalled = true
    return nil, nil
}
```

For the dnsmasq and externaldns providers, you can test by pointing them at a temporary directory and verifying the generated files:

```go
func TestDnsmasqProvider_EnsureRecords(t *testing.T) {
    tmpFile := filepath.Join(t.TempDir(), "domains.conf")
    p := dns.NewDnsmasqProvider(tmpFile)

    records := dns.RequiredRecords("example.com", "1.2.3.4")
    err := p.EnsureRecords(context.Background(), "example.com", records)
    if err != nil {
        t.Fatal(err)
    }

    content, _ := os.ReadFile(tmpFile)
    if !strings.Contains(string(content), "address=/example.com/1.2.3.4") {
        t.Error("expected A record in dnsmasq config")
    }
}
```
