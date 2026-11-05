package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// accessCookie is the cookie sign-in answers with, as tests/load/api.js reads it.
const accessCookie = "restmail_access"

// admin is a signed-in client of the admin API.
type admin struct {
	base  string
	token string
	http  *http.Client
}

// runAllow puts a sender on a domain's allowlist through the admin API, or takes
// it off with -remove. It is how a load run lets its inbound sender past the
// greylist on the testbed, the way an operator would. It skips the greylist only
// for mail whose SPF or DKIM passes and aligns, so the load's other checks still
// run. Removing a rule that is not there is not an error.
func runAllow(args []string) error {
	fs := flag.NewFlagSet("allow", flag.ExitOnError)
	api := fs.String("api", "http://10.99.0.20:8080", "API base URL")
	user := fs.String("user", "admin@restmail.test", "admin to sign in as")
	pass := fs.String("pass", "", "the admin's password")
	domain := fs.String("domain", "restmail.test", "domain whose allowlist it is")
	sender := fs.String("sender", "load@loadgen.test", "sender address, or @domain")
	remove := fs.Bool("remove", false, "take the sender off the allowlist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := &admin{base: strings.TrimRight(*api, "/"), http: &http.Client{Timeout: 30 * time.Second}}
	if err := a.signIn(*user, *pass); err != nil {
		return err
	}
	id, err := a.domainID(*domain)
	if err != nil {
		return err
	}
	if *remove {
		return a.disallow(id, *sender)
	}
	return a.allow(id, *sender)
}

func (a *admin) signIn(user, pass string) error {
	body, err := json.Marshal(map[string]string{"email": user, "password": pass})
	if err != nil {
		return err
	}
	resp, err := a.http.Post(a.base+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("sign in: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sign in as %s: %s", user, resp.Status)
	}
	for _, c := range resp.Cookies() {
		if c.Name == accessCookie {
			a.token = c.Value
			return nil
		}
	}
	return fmt.Errorf("sign in as %s: no %s cookie", user, accessCookie)
}

// do makes one request and returns its status, decoding a 2xx body into out.
func (a *admin) do(method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, a.base+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

func (a *admin) domainID(name string) (uint, error) {
	var list struct {
		Data []struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	path := "/api/v1/admin/domains"
	status, err := a.do(http.MethodGet, path, nil, &list)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("GET %s: status %d", path, status)
	}
	for _, d := range list.Data {
		if d.Name == name {
			return d.ID, nil
		}
	}
	return 0, fmt.Errorf("no domain %s among the %d listed", name, len(list.Data))
}

func allowlistPath(domain uint) string {
	return fmt.Sprintf("/api/v1/admin/domains/%d/allowlist", domain)
}

func (a *admin) allow(domain uint, sender string) error {
	path := allowlistPath(domain)
	status, err := a.do(http.MethodPost, path, map[string]string{"pattern": sender, "reason": "load run (#1583)"}, nil)
	if err != nil {
		return err
	}
	if status >= 300 && status != http.StatusConflict {
		return fmt.Errorf("POST %s: status %d", path, status)
	}
	return nil
}

func (a *admin) disallow(domain uint, sender string) error {
	var rules struct {
		Data []struct {
			ID      uint   `json:"id"`
			Pattern string `json:"pattern"`
		} `json:"data"`
	}
	path := allowlistPath(domain)
	status, err := a.do(http.MethodGet, path, nil, &rules)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", path, status)
	}
	for _, r := range rules.Data {
		if r.Pattern != sender {
			continue
		}
		rule := fmt.Sprintf("%s/%d", path, r.ID)
		status, err := a.do(http.MethodDelete, rule, nil, nil)
		if err != nil {
			return err
		}
		if status >= 300 {
			return fmt.Errorf("DELETE %s: status %d", rule, status)
		}
	}
	return nil
}
