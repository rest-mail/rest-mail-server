package e2e

import "testing"

// The suite dials a few addresses by IP. A private topology (#2062) moves them all, so each
// must come from the environment, and the shared topology's must be what an empty one gives.

func TestEndpointsKeepTheSharedAddressesByDefault(t *testing.T) {
	got := endpointsFrom(func(string) string { return "" })
	want := topologyEndpoints{
		api:        "http://10.99.0.20:8080",
		mail1IMAP:  "10.99.0.111:143",
		mail2IMAP:  "10.99.0.112:143",
		mail1POP3:  "10.99.0.111:110",
		mail1IP:    "10.99.0.11",
		mail2IP:    "10.99.0.12",
		restmailIP: "10.99.0.13",
	}
	if got != want {
		t.Fatalf("endpoints with an empty environment = %+v, want %+v", got, want)
	}
}

func TestEndpointsFollowTheEnvironment(t *testing.T) {
	env := map[string]string{
		"RESTMAIL_API_IP":  "10.153.0.20",
		"MAIL1_DOVECOT_IP": "10.153.0.111",
		"MAIL2_DOVECOT_IP": "10.153.0.112",
		"MAIL1_POSTFIX_IP": "10.153.0.11",
		"MAIL2_POSTFIX_IP": "10.153.0.12",
		"RESTMAIL_SMTP_IP": "10.153.0.13",
	}
	got := endpointsFrom(func(key string) string { return env[key] })
	want := topologyEndpoints{
		api:        "http://10.153.0.20:8080",
		mail1IMAP:  "10.153.0.111:143",
		mail2IMAP:  "10.153.0.112:143",
		mail1POP3:  "10.153.0.111:110",
		mail1IP:    "10.153.0.11",
		mail2IP:    "10.153.0.12",
		restmailIP: "10.153.0.13",
	}
	if got != want {
		t.Fatalf("endpoints = %+v, want %+v", got, want)
	}
}
