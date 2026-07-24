// Command instance renders (and later scaffolds) rest-mail instance manifests.
//
// `instance render` flattens instances/<domain>/manifest.yml into a config.env
// that the Taskfile loads via dotenv. The rendering logic lives in
// internal/instance so it is covered by the standard test lane; this file is a
// thin CLI around it.
//
// Usage:
//
//	instance render [-o out.env] [-check] <manifest.yml>
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/instance"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "render":
		renderCmd(os.Args[2:])
	case "scaffold":
		scaffoldCmd(os.Args[2:])
	case "dns-env":
		dnsEnvCmd(os.Args[2:])
	case "domains":
		domainsCmd(os.Args[2:])
	case "dkim-keygen":
		dkimKeygenCmd(os.Args[2:])
	case "dkim-provision":
		dkimProvisionCmd(os.Args[2:])
	case "dkim-check":
		dkimCheckCmd(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "instance: unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `instance — rest-mail instance manifest tool

usage:
  instance render [-o out.env] [-check] <manifest.yml>
      Flatten a manifest into a config.env (Taskfile var names).
      -o     output path (default: <manifest-dir>/config.env)
      -check don't write; exit non-zero if the target is missing or stale

  instance scaffold [-dir instances] [-profile testbed|host] <domain>
      Create instances/<domain>/{manifest.yml,config.env,secrets.env} with
      random secrets. Brings up nothing.
      -profile  substrate flavor of the generated manifest (default testbed):
                testbed  the testbed mailnet (allocates a 10.99.0.x block,
                         testbed dnsmasq/certgen) — the default; unchanged.
                host     a real host: no mailnet IPs (you assign addresses),
                         blank registry, cert_provider: manual, published
                         host ports. Fill in the placeholders, then render.

  instance dns-env [-o out.env] [-domain <name>] <manifest.yml>
      Render the dns.env consumed by reference-dnsmasq render-fragment.
      -domain  render for a specific SERVED domain (default: the primary).

  instance domains [-additional] <manifest.yml>
      List the instance's served domains, one tab-separated record per line:
      name<TAB>server_type<TAB>dkim_selector<TAB>dkim_bits<TAB>hostname.
      The primary domain is first; -additional omits it. Drives the per-domain
      DKIM/DNS provisioning loops.

  instance dkim-keygen [-selector default] [-bits 2048] <domain>
      Generate a DKIM keypair; print the private key (stdout) + the DNS
      record to publish (stderr).

  instance dkim-provision --domain <d> --admin-pass <p> [--api URL] [-bits 2048]
      Keygen + install the key on the instance via the admin API; print a
      dnsmasq txt-record line for the public record to stdout. Run where the
      API is reachable (e.g. inside the api container).

  instance dkim-check --admin-pass <p> [--api URL] <manifest.yml>
      Assert the DKIM selector provisioned in the DB for each served domain
      matches the selector the manifest declares; exit non-zero on drift. Run
      where the API is reachable (e.g. inside the api container).
`)
}

func dnsEnvCmd(args []string) {
	fs := flag.NewFlagSet("dns-env", flag.ExitOnError)
	out := fs.String("o", "", "output path (default: stdout)")
	domain := fs.String("domain", "", "render for a specific served domain (default: the primary)")
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "instance dns-env: exactly one manifest path is required")
		os.Exit(2)
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fatal("read manifest: %v", err)
	}
	m, err := instance.Parse(raw)
	if err != nil {
		fatal("%s: %v", fs.Arg(0), err)
	}
	var data []byte
	if *domain == "" {
		data, err = instance.DNSEnv(m)
	} else {
		data, err = instance.DNSEnvForDomain(m, *domain)
	}
	if err != nil {
		fatal("dns-env %s: %v", fs.Arg(0), err)
	}
	if *out == "" {
		if _, err := os.Stdout.Write(data); err != nil {
			fatal("write stdout: %v", err)
		}
		return
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fatal("write %s: %v", *out, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
}

// domainsCmd lists the instance's served domains as tab-separated records
// (name, server_type, dkim_selector, dkim_bits, hostname), one per line. The
// per-domain provisioning loops (instance:dkim, instance:dns:register) iterate
// this so structured per-domain data (selector/bits) never has to round-trip
// through flat config.env vars. -additional omits the primary.
func domainsCmd(args []string) {
	fs := flag.NewFlagSet("domains", flag.ExitOnError)
	additional := fs.Bool("additional", false, "list additional served domains only (omit the primary)")
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "instance domains: exactly one manifest path is required")
		os.Exit(2)
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fatal("read manifest: %v", err)
	}
	m, err := instance.Parse(raw)
	if err != nil {
		fatal("%s: %v", fs.Arg(0), err)
	}
	domains := m.ServedDomains()
	if *additional {
		domains = m.AdditionalServedDomains()
	}
	for _, d := range domains {
		bits := ""
		if d.Bits != nil {
			bits = strconv.Itoa(*d.Bits)
		}
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n", d.Name, d.ServerType, d.Selector, bits, d.Hostname)
	}
}

func scaffoldCmd(args []string) {
	fs := flag.NewFlagSet("scaffold", flag.ExitOnError)
	dir := fs.String("dir", "instances", "instances directory")
	profile := fs.String("profile", "testbed", "substrate profile: testbed | host")
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "instance scaffold: exactly one <domain> is required")
		os.Exit(2)
	}
	domain := fs.Arg(0)

	target := filepath.Join(*dir, domain)
	if _, err := os.Stat(target); err == nil {
		fatal("scaffold: %s already exists — refusing to overwrite", target)
	}

	res, err := instance.Scaffold(domain, *dir, *profile)
	if err != nil {
		fatal("scaffold: %v", err)
	}

	if err := os.MkdirAll(target, 0o755); err != nil {
		fatal("scaffold: %v", err)
	}
	for name, data := range map[string][]byte{
		"manifest.yml": res.Manifest,
		"config.env":   res.Config,
		"secrets.env":  res.Secrets,
	} {
		if err := os.WriteFile(filepath.Join(target, name), data, 0o644); err != nil {
			fatal("scaffold: write %s: %v", name, err)
		}
	}

	fmt.Fprintf(os.Stderr, "scaffolded %s (profile %s, project rest-mail-%s)\n", domain, res.Profile, res.Slug)
	for _, name := range []string{"postgres", "api", "smtp-gateway", "imap-gateway", "pop3-gateway", "js-filter", "webmail", "admin"} {
		ip := res.IPs[name]
		if ip == "" {
			ip = "(assign an address on your network)"
		}
		fmt.Fprintf(os.Stderr, "  %-13s %s\n", name, ip)
	}
	fmt.Fprintf(os.Stderr, "  files: %s/{manifest.yml,config.env,secrets.env}\n", target)
}

func renderCmd(args []string) {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	out := fs.String("o", "", "output path (default: <manifest-dir>/config.env)")
	check := fs.Bool("check", false, "don't write; fail if target is missing or stale")
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "instance render: exactly one manifest path is required")
		os.Exit(2)
	}
	manifestPath := fs.Arg(0)

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		fatal("read manifest: %v", err)
	}
	m, err := instance.Parse(raw)
	if err != nil {
		fatal("%s: %v", manifestPath, err)
	}
	rendered, err := instance.Render(m)
	if err != nil {
		fatal("render %s: %v", manifestPath, err)
	}

	target := *out
	if target == "" {
		target = filepath.Join(filepath.Dir(manifestPath), "config.env")
	}

	if *check {
		existing, err := os.ReadFile(target)
		if err != nil {
			fatal("check: %s missing or unreadable (run `instance render %s`): %v", target, manifestPath, err)
		}
		if !bytes.Equal(existing, rendered) {
			fatal("check: %s is stale — re-run `instance render %s`", target, manifestPath)
		}
		return
	}

	if err := os.WriteFile(target, rendered, 0o644); err != nil {
		fatal("write %s: %v", target, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", target)
}

func dkimKeygenCmd(args []string) {
	fs := flag.NewFlagSet("dkim-keygen", flag.ExitOnError)
	selector := fs.String("selector", dkim.DefaultSelector, "DKIM selector")
	bits := fs.Int("bits", 2048, "RSA key size in bits")
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "instance dkim-keygen: exactly one <domain> is required")
		os.Exit(2)
	}
	domain := fs.Arg(0)

	priv, pub, err := dkim.GenerateKey(*bits)
	if err != nil {
		fatal("dkim-keygen: %v", err)
	}
	rec, err := dkim.RecordValue(pub)
	if err != nil {
		fatal("dkim-keygen: %v", err)
	}

	// Private key → stdout (install via PUT /api/v1/admin/dkim/{id}); the DNS
	// record → stderr so `... > dkim.key` captures only the key.
	fmt.Print(priv)
	fmt.Fprintf(os.Stderr, "\n# DKIM setup for %s (selector %q):\n", domain, *selector)
	fmt.Fprintln(os.Stderr, "#   1. install the private key above via the admin API")
	fmt.Fprintf(os.Stderr, "#   2. publish DNS TXT  %s\n", dkim.RecordName(*selector, domain))
	fmt.Fprintf(os.Stderr, "#        \"%s\"\n", rec)
}

// dkimProvisionCmd generates a DKIM keypair, installs the private key on the
// instance via the admin API, and prints a dnsmasq txt-record line for the
// public record to stdout (so the caller can publish it). Meant to run where
// the API is reachable — e.g. `docker exec <project>-api go run ./cmd/instance
// dkim-provision ...`, where the API is at localhost:8080.
func dkimProvisionCmd(args []string) {
	fs := flag.NewFlagSet("dkim-provision", flag.ExitOnError)
	api := fs.String("api", "http://localhost:8080", "instance API base URL")
	domain := fs.String("domain", "", "domain to provision DKIM for")
	user := fs.String("admin-user", "admin", "admin username")
	pass := fs.String("admin-pass", "", "admin password")
	selector := fs.String("selector", dkim.DefaultSelector, "DKIM selector")
	bits := fs.Int("bits", 2048, "RSA key size in bits")
	_ = fs.Parse(args)
	if *domain == "" || *pass == "" {
		fatal("dkim-provision: --domain and --admin-pass are required")
	}

	token, err := apiLogin(*api, *user, *pass)
	if err != nil {
		fatal("dkim-provision: login: %v", err)
	}
	id, err := apiDomainID(*api, token, *domain)
	if err != nil {
		fatal("dkim-provision: find domain %q: %v", *domain, err)
	}
	priv, pub, err := dkim.GenerateKey(*bits)
	if err != nil {
		fatal("dkim-provision: keygen: %v", err)
	}
	if err := apiInstallDKIM(*api, token, id, *selector, priv); err != nil {
		fatal("dkim-provision: install key: %v", err)
	}
	rec, err := dkim.RecordValue(pub)
	if err != nil {
		fatal("dkim-provision: %v", err)
	}
	fmt.Fprintf(os.Stderr, "installed DKIM key on domain %d (selector %q)\n", id, *selector)
	// stdout: the dnsmasq txt-record line to publish.
	fmt.Println(dkim.RecordFragment(dkim.RecordName(*selector, *domain), rec))
}

// dkimCheckCmd asserts that the DKIM selector provisioned in the database for
// each served domain matches the selector the manifest declares, failing loudly
// on drift (#150). `instance render -check` only diffs the rendered config.env;
// it cannot see that a domain's DB key was provisioned under a different
// selector than the manifest names, which would silently diverge outbound
// signing and the published DNS record from the manifest. This closes that gap
// by reading the live selectors via the admin API and comparing.
func dkimCheckCmd(args []string) {
	fs := flag.NewFlagSet("dkim-check", flag.ExitOnError)
	api := fs.String("api", "http://localhost:8080", "instance API base URL")
	user := fs.String("admin-user", "admin", "admin username")
	pass := fs.String("admin-pass", "", "admin password")
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "instance dkim-check: exactly one manifest path is required")
		os.Exit(2)
	}
	if *pass == "" {
		fatal("dkim-check: --admin-pass is required")
	}

	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fatal("read manifest: %v", err)
	}
	m, err := instance.Parse(raw)
	if err != nil {
		fatal("%s: %v", fs.Arg(0), err)
	}

	token, err := apiLogin(*api, *user, *pass)
	if err != nil {
		fatal("dkim-check: login: %v", err)
	}
	provisioned, err := apiDKIMSelectors(*api, token)
	if err != nil {
		fatal("dkim-check: list DKIM keys: %v", err)
	}

	drift := m.CheckDKIMSelectorDrift(provisioned, dkim.DefaultSelector)
	if len(drift) > 0 {
		fmt.Fprintf(os.Stderr, "instance: DKIM selector drift detected (%d domain(s)):\n", len(drift))
		for _, d := range drift {
			fmt.Fprintf(os.Stderr, "  %s\n", d.String())
		}
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "DKIM selectors match the manifest for all %d served domain(s)\n", len(m.ServedDomains()))
}

// apiDKIMSelectors fetches the provisioned DKIM selectors from the admin API and
// returns them keyed by domain name. Only domains with an installed key are
// included (GET /api/v1/admin/dkim already filters to those), so a served domain
// absent from the map has no provisioned key.
func apiDKIMSelectors(api, token string) (map[string]string, error) {
	var res struct {
		Data []struct {
			Domain   string `json:"domain"`
			Selector string `json:"selector"`
			HasKey   bool   `json:"has_key"`
		} `json:"data"`
	}
	if err := apiJSON(http.MethodGet, api+"/api/v1/admin/dkim", token, nil, &res); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(res.Data))
	for _, e := range res.Data {
		if e.HasKey {
			out[e.Domain] = e.Selector
		}
	}
	return out, nil
}

func apiJSON(method, url, token string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, url, resp.StatusCode, string(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func apiLogin(api, user, pass string) (string, error) {
	var res struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := apiJSON(http.MethodPost, api+"/api/v1/auth/login", "",
		map[string]string{"username": user, "password": pass}, &res); err != nil {
		return "", err
	}
	if res.Data.AccessToken == "" {
		return "", fmt.Errorf("no access token in response")
	}
	return res.Data.AccessToken, nil
}

func apiDomainID(api, token, domain string) (uint, error) {
	var res struct {
		Data []struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := apiJSON(http.MethodGet, api+"/api/v1/admin/domains", token, nil, &res); err != nil {
		return 0, err
	}
	for _, d := range res.Data {
		if d.Name == domain {
			return d.ID, nil
		}
	}
	return 0, fmt.Errorf("domain not found (is the instance seeded?)")
}

func apiInstallDKIM(api, token string, id uint, selector, privateKeyPEM string) error {
	url := fmt.Sprintf("%s/api/v1/admin/dkim/%d", api, id)
	return apiJSON(http.MethodPut, url, token,
		map[string]string{"selector": selector, "private_key": privateKeyPEM}, nil)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "instance: "+format+"\n", args...)
	os.Exit(1)
}
