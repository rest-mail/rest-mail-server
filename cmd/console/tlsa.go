package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/restmail/restmail/internal/dns"
)

const tlsaUsage = `restmail tlsa-records — prepare SMTP TLSA records from public certificates

  restmail tlsa-records --mail-host mx.example.com --cert current.pem [--next-cert staged.pem]

Prints 3 1 1 (DANE-EE, SPKI, SHA-256) records for port 25. A PEM chain must
start with the serving certificate. No private key is required. --next-cert
includes a staged key alongside the current one for a planned rollover.

Publish only in a DNSSEC-signed zone with a validated chain of trust. Before
activating a new key, publish both records and wait for the previous TLSA
and signature caches to expire. Keep records for every key still serving.
This command changes no DNS, certificate or renewal configuration. Automatic
renewal currently rotates keys: do not publish these records for an unmanaged
automatic renewal. See docs/dane-tlsa.md.

  --mail-host HOST   the MX hostname, not the recipient domain
  --cert PATH        current public certificate (leaf first)
  --next-cert PATH   optional staged public certificate (leaf first)
  --json             one JSON object with records and publication requirements

Exit status: 0 prepared, 2 usage or invalid certificate, 1 file read failed.
`

const tlsaRollover = "Publish current and staged keys together; wait for the previous TLSA and RRSIG caches to expire before activating the staged key. Keep records for all keys still serving."

func runTLSARecords(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tlsa-records", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	host := fs.String("mail-host", "", "the MX hostname")
	certPath := fs.String("cert", "", "current public certificate")
	nextPath := fs.String("next-cert", "", "staged public certificate")
	asJSON := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")
	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if err := fs.Parse(args); err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, tlsaUsage)
		return exitOK
	}
	if *host == "" || *certPath == "" || fs.NArg() != 0 {
		return o.fail(exitUsage, "bad_flag", "give --mail-host and --cert, with optional --next-cert")
	}
	var certs [][]byte
	for _, path := range []string{*certPath, *nextPath} {
		if path == "" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return o.fail(exitFailed, "read_failed", "could not open a public certificate file")
		}
		certPEM, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
		_ = f.Close()
		if err != nil || len(certPEM) > 1<<20 {
			return o.fail(exitFailed, "read_failed", "could not read a public certificate file of at most 1 MiB")
		}
		certs = append(certs, certPEM)
	}
	records, err := dns.SMTPTLSARecords(*host, certs...)
	if err != nil {
		return o.fail(exitUsage, "invalid_certificate", err.Error())
	}
	if o.json {
		_ = json.NewEncoder(stdout).Encode(map[string]any{
			"records": records, "dnssec_required": true, "automatic_rollover": false, "rollover": tlsaRollover,
		})
	} else {
		fmt.Fprintln(stdout, "; Requires DNSSEC with a validated chain of trust; review the certificate renewal/key rollover plan.")
		fmt.Fprintln(stdout, "; "+tlsaRollover)
		for _, record := range records {
			fmt.Fprintf(stdout, "%s. %d IN TLSA %s\n", record.Name, record.TTL, record.Value)
		}
	}
	return exitOK
}
