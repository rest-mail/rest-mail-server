package rfctest

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

type appendUIDMailbox struct {
	*appendMailbox
	uid, validity uint32
	validityErr   error
	validityCalls []string
	uidCalls      int
}

func (m *appendUIDMailbox) UIDValidity(folder string) (uint32, error) {
	m.validityCalls = append(m.validityCalls, folder)
	return m.validity, m.validityErr
}
func (m *appendUIDMailbox) AppendUID(folder string, flags imap.FlagUpdate, raw []byte) (uint32, error) {
	m.uidCalls++
	return m.uid, m.Append(folder, flags, raw)
}
func (*appendUIDMailbox) CopyUID(uint32, string) (uint32, error) {
	return 0, errors.New("unused")
}

type datedAppendUIDMailbox struct{ *appendUIDMailbox }

func (m *datedAppendUIDMailbox) AppendWithDate(folder string, flags imap.FlagUpdate, date time.Time, raw []byte) (uint32, error) {
	m.dateCalls++
	err := m.Append(folder, flags, raw)
	if err == nil {
		m.records[len(m.records)-1].date = date
	}
	// Deliberately differs from AppendUID's result to prove which path won.
	return 3956, err
}

// checklist: IMAP-UIDPLUS-CAP-01
func TestUIDPlusAdvertisement(t *testing.T) {
	for _, box := range []imap.Mailbox{mailbox{}, &appendUIDMailbox{appendMailbox: &appendMailbox{}}} {
		c := dialMailbox(t, box)
		c.line()
		if slices.Contains(c.capabilities("c0"), "UIDPLUS") {
			t.Fatal("UIDPLUS advertised before the account is known")
		}
		c.login()
		_, supported := box.(imap.UIDPlusMailbox)
		for _, selectBox := range []bool{false, true} {
			if selectBox {
				_, st := c.command("s", "SELECT INBOX")
				requireOK(t, st, "s")
			}
			if got := slices.Contains(c.capabilities("c"), "UIDPLUS"); got != supported {
				t.Fatalf("UIDPLUS = %v, backend support = %v", got, supported)
			}
		}
	}
}

// Uses RFC 4315 section 3's destination validity/UID pair and mailbox; the
// message is synthetic, since this proof concerns the completion metadata.
//
// checklist: IMAP-APPENDUID-01 IMAP-APPENDUID-02
// checklist: IMAP2-RESP-174
func TestAppendUIDResponse(t *testing.T) {
	for _, selected := range []bool{false, true} {
		for _, dated := range []bool{false, true} {
			m := &appendUIDMailbox{appendMailbox: &appendMailbox{}, uid: 3955, validity: 38505}
			var box imap.Mailbox = m
			args, wantUID := `saved-messages (\Seen)`, "3955"
			if dated {
				box = &datedAppendUIDMailbox{m}
				args += ` "07-Feb-1994 21:52:25 -0800"`
				wantUID = "3956"
			}
			c := selectionClient(t, box)
			if selected {
				_, st := c.command("s", "SELECT INBOX")
				requireOK(t, st, "s")
			}
			m.validityCalls = nil
			lines, st := appendCommand(t, c, args, "Subject: proof\r\n\r\nbody\r\n")
			requireExactly(t, lines)
			if want := "A1 OK [APPENDUID 38505 " + wantUID + "] APPEND completed"; st != want {
				t.Fatalf("completion = %q, want %q", st, want)
			}
			if !slices.Equal(m.validityCalls, []string{"saved-messages"}) {
				t.Fatalf("UIDVALIDITY queried for %q", m.validityCalls)
			}
			if len(m.records) != 1 || m.records[0].folder != "saved-messages" || m.records[0].raw != "Subject: proof\r\n\r\nbody\r\n" || m.records[0].flags.Seen == nil || !*m.records[0].flags.Seen {
				t.Fatalf("stored message = %+v", m.records)
			}
			if dated && (m.dateCalls != 1 || m.uidCalls != 0 || m.records[0].date.Format("02-Jan-2006 15:04:05 -0700") != "07-Feb-1994 21:52:25 -0800") {
				t.Fatalf("date dispatch: date=%d uid=%d records=%+v", m.dateCalls, m.uidCalls, m.records)
			}
			if !dated && (m.uidCalls != 1 || m.dateCalls != 0) {
				t.Fatalf("UID dispatch: date=%d uid=%d", m.dateCalls, m.uidCalls)
			}
		}
	}
}

// checklist: IMAP-APPENDUID-03
func TestAppendUIDUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name          string
		uid, validity uint32
		err           error
	}{
		{"unknown UID", 0, 38505, nil},
		{"unknown validity", 3955, 0, nil},
		{"validity error", 3955, 38505, errors.New("private validity error")},
		{"maximum values", ^uint32(0), ^uint32(0), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &appendUIDMailbox{appendMailbox: &appendMailbox{}, uid: tc.uid, validity: tc.validity, validityErr: tc.err}
			c := selectionClient(t, m)
			lines, st := appendCommand(t, c, "Sent", "message")
			requireExactly(t, lines)
			want := "A1 OK APPEND completed"
			if tc.name == "maximum values" {
				want = "A1 OK [APPENDUID 4294967295 4294967295] APPEND completed"
			}
			if st != want || len(m.records) != 1 {
				t.Fatalf("completion=%q records=%+v, want %q and one delivery", st, m.records, want)
			}
			_, st = c.command("n", "NOOP")
			requireOK(t, st, "n")
		})
	}
	c := selectionClient(t, &appendMailbox{})
	_, st := appendCommand(t, c, "Sent", "message")
	if st != "A1 OK APPEND completed" {
		t.Fatalf("base backend completion: %q", st)
	}
}

// checklist: IMAP-APPENDUID-04
func TestAppendUIDFailureAndRetry(t *testing.T) {
	for _, dated := range []bool{false, true} {
		m := &appendUIDMailbox{appendMailbox: &appendMailbox{fail: true}, uid: 3955, validity: 38505}
		var box imap.Mailbox = m
		args := "Sent"
		if dated {
			box = &datedAppendUIDMailbox{m}
			args += ` "07-Feb-1994 21:52:25 -0800"`
		}
		c := selectionClient(t, box)
		lines, st := appendCommand(t, c, args, "message")
		requireExactly(t, lines)
		if st != "A1 NO APPEND failed" || len(m.records) != 0 || len(m.validityCalls) != 0 {
			t.Fatalf("failed append: %q records=%+v validity=%q", st, m.records, m.validityCalls)
		}
		m.fail = false
		_, st = appendCommand(t, c, args, "retry")
		if !strings.HasPrefix(st, "A1 OK [APPENDUID 38505 ") || len(m.records) != 1 || m.records[0].raw != "retry" {
			t.Fatalf("retry: %q records=%+v", st, m.records)
		}
	}
}
