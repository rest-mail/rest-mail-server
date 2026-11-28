package rfctest

import "testing"

// RFC 3206 §5 strongly recommends never sending the AUTH code to USER, since
// it reveals that the user exists. Every USER reply is pinned exactly: +OK for
// known and unknown names, and the three refusals (no argument, TLS required,
// already authenticated) carry no response code at all.
// checklist: POP3-RESP-CODES-09 POP3-RESP-CODES-10 POP3-ITLS-12
func TestRFC3206NoAuthCodeOnUser(t *testing.T) {
	w := connect(t, &backend{password: "secret"})
	expect(t, w.cmd("USER"), "-ERR Username required")
	expect(t, w.cmd("USER nobody"), "+OK")
	expect(t, w.cmd("USER alice"), "+OK")
	w.login("secret")
	expect(t, w.cmd("USER alice"), "-ERR Already authenticated")

	server, _ := stlsConfigs(t)
	w, b := stlsConnect(t, server)
	expect(t, w.cmd("USER alice"), "-ERR TLS required")
	expect(t, w.cmd("USER nobody"), "-ERR TLS required")
	if b.calls.Load() != 0 {
		t.Fatalf("USER reached the Backend: %d calls", b.calls.Load())
	}
}
