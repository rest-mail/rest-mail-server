package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

const importAliasesUsage = `restmail import-aliases — import a Postfix virtual alias map

  restmail import-aliases --server https://mail.example.com /etc/postfix/virtual

Reads source-address whitespace destination-address[, destination-address ...].
Blank lines, comment lines and indented continuations follow Postfix virtual(5).
Use this text format for bundle exports (including postfix-virtual.cf); Dovecot
passwd-files contain accounts, not aliases. Import the destination accounts first.
RESTMAIL_API_KEY needs accounts:read and accounts:write on the source domains,
and accounts:read on destination accounts. Every write uses the admin alias API.
Only active, readable hosted mailbox destinations are supported. External forwards,
alias chains, catch-alls, self redirects and conflicting/inactive rows are refused.
No existing rule is changed. Delivery is separate; see docs/migrating-accounts.md.

  --server URL     the server's address (https; http only for localhost)
  --domain DOMAIN  qualify bare source and destination user names
  --dry-run        syntax checks only; no API calls or destination/policy checks
  --json           one JSON object listing rows by outcome

Name one file or pipe it on stdin. Maximum 16 MiB per file, 64 KiB per logical
line. Failures name the starting physical line and do not stop later lines.
Exit status: 0 created/already present, 2 usage, 1 any line failed.
`

type importedAlias struct {
	ID                 uint   `json:"id"`
	SourceAddress      string `json:"source_address"`
	DestinationAddress string `json:"destination_address"`
	Active             bool   `json:"active"`
}

type aliasImportResult struct {
	Line               int    `json:"line"`
	SourceAddress      string `json:"source_address,omitempty"`
	DestinationAddress string `json:"destination_address,omitempty"`
	ID                 uint   `json:"id,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

type aliasImportLine struct {
	line         int
	source       string
	destinations []string
	reason       string
}

func runImportAliases(ctx context.Context, args []string, stdin io.Reader, token string, client *http.Client, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("import-aliases", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	server := flags.String("server", "", "server address")
	domain := flags.String("domain", "", "domain for bare users")
	dryRun := flags.Bool("dry-run", false, "syntax checks only")
	asJSON := flags.Bool("json", false, "JSON output")
	help := flags.Bool("help", false, "show help")
	output := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if err := flags.Parse(args); err != nil {
		return output.fail(exitUsage, "bad_flag", err.Error())
	}
	output.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, importAliasesUsage)
		return exitOK
	}
	base, err := serverURL(*server)
	if err != nil && !(*dryRun && *server == "") {
		return output.fail(exitUsage, "bad_flag", err.Error())
	}
	if *domain != "" {
		if _, err := rmail.CanonicalMailboxAddress("postmaster@" + *domain); err != nil {
			return output.fail(exitUsage, "bad_flag", "give --domain as a domain name")
		}
	}
	if token == "" && !*dryRun {
		return output.fail(exitUsage, "no_api_key", "set RESTMAIL_API_KEY to an administrator session or scoped key with accounts:read and accounts:write")
	}
	var reader io.Reader = stdin
	switch flags.NArg() {
	case 0:
		if isTerminal(stdin) {
			return output.fail(exitUsage, "no_file", "name the alias map, or pipe it on stdin")
		}
	case 1:
		file, err := os.Open(flags.Arg(0))
		if err != nil {
			return output.fail(exitUsage, "no_file", "could not open the alias map")
		}
		defer file.Close()
		reader = file
	default:
		return output.fail(exitUsage, "bad_flag", "name at most one alias map")
	}
	const maxFile = 16 << 20
	raw, err := io.ReadAll(io.LimitReader(reader, maxFile+1))
	if err != nil || len(raw) > maxFile {
		return output.fail(exitFailed, "invalid_file", "could not read an alias map of at most 16 MiB; nothing imported")
	}
	outcome := map[string][]aliasImportResult{"created": {}, "existing": {}, "would_create": {}, "failed": {}}
	var snapshot *aliasImportSnapshot
	var snapshotErr error
	loaded := false
	for _, line := range parseAliasImportMap(raw, *domain) {
		result := aliasImportResult{Line: line.line, SourceAddress: line.source, Reason: line.reason}
		if result.Reason != "" {
			outcome["failed"] = append(outcome["failed"], result)
			continue
		}
		if !*dryRun {
			if !loaded {
				snapshot, snapshotErr = loadAliasImportSnapshot(ctx, client, base, token)
				loaded = true
			}
			if snapshotErr != nil {
				result.Reason = snapshotErr.Error()
			} else {
				result.Reason = snapshot.check(line)
			}
			if result.Reason != "" {
				outcome["failed"] = append(outcome["failed"], result)
				continue
			}
		}
		for _, destination := range line.destinations {
			result := aliasImportResult{Line: line.line, SourceAddress: line.source, DestinationAddress: destination}
			if *dryRun {
				outcome["would_create"] = append(outcome["would_create"], result)
				continue
			}
			if row, exists := snapshot.existing(line.source, destination); exists {
				result.ID = row.ID
				outcome["existing"] = append(outcome["existing"], result)
				continue
			}
			body, _ := json.Marshal(map[string]string{"source_address": line.source, "destination_address": destination})
			var response endpoint.Data[importedAlias]
			status, err := aliasImportRequest(ctx, client, base, token, http.MethodPost, "/api/v1/admin/aliases", body, &response)
			kind := "created"
			if err == nil && status == http.StatusConflict {
				rows, readErr := listAliasImportResource[importedAlias](ctx, client, base, token, "/api/v1/admin/aliases")
				if readErr == nil {
					snapshot.aliases = rows
					if row, exists := snapshot.existing(line.source, destination); exists && snapshot.check(line) == "" {
						response.Data = row
						status, kind = http.StatusCreated, "existing"
					}
				}
			}
			switch {
			case err != nil:
				result.Reason = err.Error()
			case status != http.StatusCreated:
				result.Reason = fmt.Sprintf("the server refused the import (HTTP %d)", status)
			case response.Data.ID == 0 || !response.Data.Active || response.Data.SourceAddress != line.source || response.Data.DestinationAddress != destination:
				result.Reason = "the server returned an invalid alias response; inspect stored rows before retrying"
			default:
				result.ID = response.Data.ID
				if kind == "created" {
					snapshot.aliases = append(snapshot.aliases, response.Data)
				}
			}
			if result.Reason != "" {
				kind = "failed"
			}
			outcome[kind] = append(outcome[kind], result)
		}
	}
	if output.json {
		_ = json.NewEncoder(stdout).Encode(outcome)
	} else {
		for _, result := range outcome["failed"] {
			fmt.Fprintf(stderr, "restmail: line %d %s: %s\n", result.Line, result.SourceAddress, result.Reason)
		}
		if *dryRun {
			fmt.Fprintf(stdout, "Would create %d aliases; %d failed. Syntax checks only.\n", len(outcome["would_create"]), len(outcome["failed"]))
		} else {
			fmt.Fprintf(stdout, "Created %d aliases; %d already existed and were left alone; %d failed.\n", len(outcome["created"]), len(outcome["existing"]), len(outcome["failed"]))
		}
	}
	if len(outcome["failed"]) != 0 {
		return exitFailed
	}
	return exitOK
}

func parseAliasImportMap(raw []byte, domain string) []aliasImportLine {
	var lines []aliasImportLine
	var pending string
	start, physical := 0, 0
	flush := func() {
		if start == 0 {
			return
		}
		line := aliasImportLine{line: start}
		if len(pending) > 64<<10 {
			line.reason = "logical line exceeds 64 KiB"
		} else {
			line.source, line.destinations, line.reason = parseAliasImportLine(pending, domain)
		}
		lines = append(lines, line)
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), len(raw)+1)
	for scanner.Scan() {
		physical++
		text := scanner.Text()
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(text, " ") || strings.HasPrefix(text, "\t") {
			if start == 0 {
				lines = append(lines, aliasImportLine{line: physical, reason: "continuation without a preceding alias"})
			} else if len(pending) <= 64<<10 {
				pending += " " + trimmed
			}
			continue
		}
		flush()
		start, pending = physical, text
	}
	flush()
	return lines
}

func parseAliasImportLine(text, domain string) (string, []string, string) {
	separator := strings.IndexAny(text, " \t")
	if separator < 1 {
		return "", nil, "expected source-address whitespace destination-address[, destination-address ...]"
	}
	qualify := func(address string) (string, error) {
		address = strings.TrimSpace(address)
		if !strings.Contains(address, "@") && domain != "" {
			address += "@" + domain
		}
		return rmail.CanonicalMailboxAddress(address)
	}
	source, err := qualify(text[:separator])
	if err != nil {
		return "", nil, "source must be a bare mail address; catch-alls and patterns are unsupported (bare users need --domain)"
	}
	var destinations []string
	seen := map[string]bool{}
	for _, value := range strings.Split(text[separator+1:], ",") {
		destination, err := qualify(value)
		if err != nil || destination == source {
			return source, nil, "destinations must be bare mail addresses, not self redirects, commands, files or patterns"
		}
		if !seen[destination] {
			seen[destination] = true
			destinations = append(destinations, destination)
		}
	}
	return source, destinations, ""
}

type aliasImportSnapshot struct {
	mailboxes map[string]bool
	aliases   []importedAlias
}

func loadAliasImportSnapshot(ctx context.Context, client *http.Client, base *url.URL, token string) (*aliasImportSnapshot, error) {
	mailboxes, err := listAliasImportResource[struct {
		Address string `json:"address"`
		Active  bool   `json:"active"`
	}](ctx, client, base, token, "/api/v1/admin/mailboxes")
	if err != nil {
		return nil, err
	}
	aliases, err := listAliasImportResource[importedAlias](ctx, client, base, token, "/api/v1/admin/aliases")
	if err != nil {
		return nil, err
	}
	snapshot := &aliasImportSnapshot{mailboxes: map[string]bool{}, aliases: aliases}
	for _, mailbox := range mailboxes {
		if canonical, err := rmail.CanonicalMailboxAddress(mailbox.Address); err == nil && mailbox.Active {
			snapshot.mailboxes[canonical] = true
		}
	}
	return snapshot, nil
}

func (snapshot *aliasImportSnapshot) check(line aliasImportLine) string {
	wanted := map[string]bool{}
	for _, destination := range line.destinations {
		if !snapshot.mailboxes[destination] {
			return "destination is not an active readable hosted mailbox; external forwarding awaits policy, and alias chains are unsupported"
		}
		wanted[destination] = true
	}
	for _, row := range snapshot.aliases {
		source, err := rmail.CanonicalMailboxAddress(row.SourceAddress)
		if err != nil || source != line.source {
			continue
		}
		if !row.Active || !wanted[row.DestinationAddress] || row.SourceAddress != line.source {
			return "source has conflicting, inactive or noncanonical aliases; existing rules were left alone"
		}
	}
	return ""
}

func (snapshot *aliasImportSnapshot) existing(source, destination string) (importedAlias, bool) {
	for _, row := range snapshot.aliases {
		if row.Active && row.SourceAddress == source && row.DestinationAddress == destination {
			return row, true
		}
	}
	return importedAlias{}, false
}

func listAliasImportResource[Row any](ctx context.Context, client *http.Client, base *url.URL, token, path string) ([]Row, error) {
	var rows []Row
	for offset := 0; offset < 1_000_000; {
		var response endpoint.List[Row]
		status, err := aliasImportRequest(ctx, client, base, token, http.MethodGet, fmt.Sprintf("%s?limit=100&offset=%d", path, offset), nil, &response)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("could not read existing accounts/aliases (HTTP %d); check accounts:read scope", status)
		}
		if response.Pagination == nil || (response.Pagination.HasMore && len(response.Data) == 0) {
			return nil, fmt.Errorf("the server returned invalid accounts/aliases pagination")
		}
		rows = append(rows, response.Data...)
		if !response.Pagination.HasMore {
			return rows, nil
		}
		offset += len(response.Data)
	}
	return nil, fmt.Errorf("accounts/aliases listing exceeds one million rows")
}

func aliasImportRequest(ctx context.Context, client *http.Client, base *url.URL, token, method, path string, body []byte, result any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, method, base.String()+path, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("could not prepare the alias import request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := noRedirect.Do(request)
	if err != nil {
		return 0, fmt.Errorf("the alias import request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return response.StatusCode, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, result) != nil {
		return response.StatusCode, fmt.Errorf("the server returned an invalid alias import response; inspect stored rows before retrying")
	}
	return response.StatusCode, nil
}
