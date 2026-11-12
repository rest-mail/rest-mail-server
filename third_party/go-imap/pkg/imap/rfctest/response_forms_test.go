package rfctest

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// registeredCapabilities are capability names in the IANA IMAP capabilities
// registry. RFC 3501 §7.2.1 says a capability name other than one beginning "X"
// or "AUTH=" must be a registered one. A name beginning "AUTH=" is checked apart.
var registeredCapabilities = []string{
	"ACL", "APPENDLIMIT", "BINARY", "CATENATE", "CHILDREN", "COMPRESS=DEFLATE", "CONDSTORE", "CONTEXT=SEARCH",
	"CONTEXT=SORT", "CONVERT", "CREATE-SPECIAL-USE", "ENABLE", "ESEARCH", "ESORT", "FILTERS", "I18NLEVEL=1",
	"I18NLEVEL=2", "ID", "IDLE", "IMAP4rev1", "IMAP4rev2", "INBOX-REFERRALS", "LANGUAGE", "LIST-EXTENDED",
	"LIST-METADATA", "LIST-MYRIGHTS", "LIST-STATUS", "LITERAL+", "LITERAL-", "LOGIN-REFERRALS", "LOGINDISABLED",
	"MAILBOX-REFERRALS", "METADATA", "METADATA-SERVER", "MOVE", "MULTIAPPEND", "MULTISEARCH", "NAMESPACE", "NOTIFY",
	"OBJECTID", "PREVIEW", "QRESYNC", "QUOTA", "QUOTASET", "REPLACE", "SASL-IR", "SAVEDATE", "SEARCH=FUZZY",
	"SEARCHRES", "SORT", "SORT=DISPLAY", "SPECIAL-USE", "STARTTLS", "STATUS=SIZE", "THREAD=ORDEREDSUBJECT",
	"THREAD=REFERENCES", "UIDONLY", "UIDPLUS", "UNAUTHENTICATE", "UNSELECT", "URL-PARTIAL", "URLAUTH", "UTF8=ACCEPT",
	"UTF8=ONLY", "WITHIN",
}

// requireCapabilityList applies §9's capability-data: "IMAP4rev1" among
// space-separated atoms, a new name registered or starting "X", and no "IMAP4",
// which only a server offering RFC 1730 compatibility lists, first. The reader
// has already refused a name that is not an atom.
func requireCapabilityList(t *testing.T, names []string) {
	t.Helper()
	if !slices.Contains(names, "IMAP4rev1") {
		t.Fatalf("no IMAP4rev1 in %q", names)
	}
	if slices.Contains(names, "IMAP4") {
		t.Fatalf("IMAP4 claims RFC 1730 compatibility: %q", names)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			t.Fatalf("%s listed twice in %q", name, names)
		}
		seen[name] = true
		if strings.HasPrefix(name, "AUTH=") {
			if len(name) == len("AUTH=") {
				t.Fatalf("AUTH= without a mechanism in %q", names)
			}
			continue
		}
		if !strings.HasPrefix(name, "X") && !slices.Contains(registeredCapabilities, name) {
			t.Fatalf("%s is neither registered nor X-prefixed, in %q", name, names)
		}
	}
}

// TestCapabilityForms proves §9's capability-data everywhere a list appears: the
// CAPABILITY response and the code on the greeting and on LOGIN's OK, with and
// without a TLS requirement. The list keeps its rules as it changes with the
// connection's state.
//
// checklist: IMAP-INV-ABNF-07
func TestCapabilityForms(t *testing.T) {
	check := func(t *testing.T, c *client) {
		t.Helper()
		responses := c.exchange("C1", "CAPABILITY")
		if len(responses) != 2 || responses[0].kind != "CAPABILITY" || responses[1].kind != "OK" {
			t.Fatalf("CAPABILITY: %+v", responses)
		}
		requireCapabilityList(t, responses[0].words)
	}
	t.Run("before and after login", func(t *testing.T) {
		c := dialMailbox(t, nil)
		greeting := c.response()
		if greeting.code != "CAPABILITY" {
			t.Fatalf("greeting: %+v", greeting)
		}
		requireCapabilityList(t, greeting.words)
		check(t, c)
		responses := c.exchange("L1", "LOGIN %s %s", testUser, testPass)
		login := responses[len(responses)-1]
		if login.kind != "OK" {
			t.Fatalf("LOGIN: %+v", login)
		}
		if login.code == "CAPABILITY" {
			requireCapabilityList(t, login.words)
		}
		check(t, c)
	})
	t.Run("TLS required", func(t *testing.T) {
		c := dialTLS(t, nil, nil, serverTLS(t))
		greeting := c.response()
		requireCapabilityList(t, greeting.words)
		for _, name := range []string{"STARTTLS", "LOGINDISABLED"} {
			if !slices.Contains(greeting.words, name) {
				t.Fatalf("no %s in %q", name, greeting.words)
			}
		}
		check(t, c)
	})
	t.Run("optional interfaces", func(t *testing.T) {
		c := selectionClient(t, &exampleMailbox{selectionMailbox: &selectionMailbox{}})
		responses := c.exchange("C1", "CAPABILITY")
		requireCapabilityList(t, responses[0].words)
		if !slices.Contains(responses[0].words, "UIDPLUS") {
			t.Fatalf("no UIDPLUS in %q", responses[0].words)
		}
	})
}

// TestResponseForms proves §9's response framing through a whole session. The
// reader takes "+" SP for a continuation, "*" SP for data or BYE, and the
// command's tag for its completion, each ending in CRLF and with text that is
// not empty. A greeting is OK, and PREAUTH is not offered. After a fatal BYE the
// connection closes at once.
//
// checklist: IMAP-INV-ABNF-11
func TestResponseForms(t *testing.T) {
	t.Run("data and completion", func(t *testing.T) {
		c := dialMailbox(t, newFetchMailbox())
		if greeting := c.response(); greeting.tag != "*" || greeting.kind != "OK" {
			t.Fatalf("greeting: %+v", greeting)
		}
		for number, command := range []string{
			"CAPABILITY", "NOOP", "FROBNICATE", "SELECT INBOX", "LOGIN nobody nothing",
			"LOGIN " + testUser + " " + testPass, "LOGIN " + testUser + " " + testPass,
			`LIST "" "*"`, `LSUB "" "*"`, "STATUS INBOX (MESSAGES)", "SELECT", "SELECT Missing",
			"EXAMINE INBOX", "SELECT INBOX", "NOOP", "CHECK", "FETCH 1 ALL", "FETCH 1 BODY[]",
			"FETCH 1 BOGUS", "SEARCH ALL", "SEARCH CHARSET BOGUS ALL", `STORE 1 +FLAGS (\Flagged)`,
			"COPY 1 INBOX", "UID FETCH 10 FLAGS", "EXPUNGE", "CLOSE", "NOOP",
		} {
			tag := fmt.Sprintf("T%d", number)
			responses := c.exchange(tag, "%s", command)
			last := responses[len(responses)-1]
			if last.tag != tag || last.text == "" {
				t.Fatalf("%s: %+v", command, last)
			}
		}
	})
	t.Run("literal continuation", func(t *testing.T) {
		c := dialMailbox(t, newFetchMailbox())
		c.response()
		write(t, c, fmt.Sprintf("L1 LOGIN {%d}\r\n", len(testUser)))
		if response := c.response(); response.tag != "+" || response.text == "" {
			t.Fatalf("continuation: %+v", response)
		}
		write(t, c, testUser+" "+testPass+"\r\n")
		if responses := c.responsesThrough("L1"); responses[len(responses)-1].kind != "OK" {
			t.Fatalf("LOGIN: %+v", responses)
		}
	})
	t.Run("AUTHENTICATE continuation", func(t *testing.T) {
		c := dialMailbox(t, newFetchMailbox())
		c.response()
		write(t, c, "A1 AUTHENTICATE PLAIN\r\n")
		if response := c.response(); response.tag != "+" {
			t.Fatalf("continuation: %+v", response)
		}
		write(t, c, base64.StdEncoding.EncodeToString([]byte("\x00"+testUser+"\x00"+testPass))+"\r\n")
		if responses := c.responsesThrough("A1"); responses[len(responses)-1].kind != "OK" {
			t.Fatalf("AUTHENTICATE: %+v", responses)
		}
	})
	t.Run("IDLE continuation", func(t *testing.T) {
		c := fetchClient(t, newFetchMailbox(), "SELECT")
		write(t, c, "I1 IDLE\r\n")
		if response := c.response(); response.tag != "+" || response.text == "" {
			t.Fatalf("continuation: %+v", response)
		}
		write(t, c, "DONE\r\n")
		if responses := c.responsesThrough("I1"); responses[len(responses)-1].kind != "OK" {
			t.Fatalf("IDLE: %+v", responses)
		}
	})
	t.Run("untagged BAD", func(t *testing.T) {
		c := dialMailbox(t, newFetchMailbox())
		c.response()
		write(t, c, "\r\n")
		if response := c.response(); response.tag != "*" || response.kind != "BAD" {
			t.Fatalf("empty line: %+v", response)
		}
	})
	t.Run("LOGOUT", func(t *testing.T) {
		c := selectionClient(t, newFetchMailbox())
		responses := c.exchange("Z1", "LOGOUT")
		if len(responses) != 2 || responses[0].kind != "BYE" || responses[1].kind != "OK" {
			t.Fatalf("LOGOUT: %+v", responses)
		}
		if !c.closed() {
			t.Fatal("connection open after LOGOUT")
		}
	})
	t.Run("fatal BYE", func(t *testing.T) {
		c := selectionClient(t, &appendMailbox{})
		write(t, c, "A1 APPEND INBOX {3}\r\n")
		if response := c.response(); response.tag != "+" {
			t.Fatalf("continuation: %+v", response)
		}
		write(t, c, "abcxx")
		if response := c.response(); response.tag != "A1" || response.kind != "BAD" {
			t.Fatalf("broken framing: %+v", response)
		}
		if response := c.response(); response.kind != "BYE" {
			t.Fatalf("after broken framing: %+v", response)
		}
		if !c.closed() {
			t.Fatal("connection open after BYE")
		}
	})
}

func write(t *testing.T, c *client, text string) {
	t.Helper()
	if _, err := io.WriteString(c.conn, text); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// namedFolders holds folders whose names need each framing, and records the
// folder name each operation reaches the store with.
type namedFolders struct {
	mailbox
	folders []string
	reached []string
}

func (m *namedFolders) Folders() ([]imap.Folder, error) {
	folders := make([]imap.Folder, 0, len(m.folders))
	for _, name := range m.folders {
		folders = append(folders, imap.Folder{Name: name})
	}
	return folders, nil
}

func (m *namedFolders) Messages(folder string) ([]imap.Message, error) {
	m.reached = append(m.reached, folder)
	if !slices.Contains(m.folders, folder) {
		return nil, errors.New("mailbox unavailable")
	}
	return []imap.Message{{UID: 10, Size: 5, Date: time.Unix(0, 0)}}, nil
}

func (m *namedFolders) Copy(_ uint32, dest string) error {
	m.reached = append(m.reached, dest)
	return nil
}

func (m *namedFolders) Append(dest string, _ imap.FlagUpdate, _ []byte) error {
	m.reached = append(m.reached, dest)
	return nil
}

// TestInboxInEveryCase proves §9's mailbox: "All case variants of INBOX (e.g.
// "iNbOx") MUST be interpreted as INBOX not as an astring", for each command that
// names a mailbox. Another name keeps its case.
//
// checklist: IMAP-INV-ABNF-17
func TestInboxInEveryCase(t *testing.T) {
	for _, name := range []string{"INBOX", "inbox", "iNbOx", `"InBoX"`, "{5}\r\ninbOX"} {
		t.Run(name, func(t *testing.T) {
			box := &namedFolders{folders: []string{"INBOX", "Work"}}
			c := selectionClient(t, box)
			for number, form := range []string{"SELECT %s", "EXAMINE %s", "STATUS %s (MESSAGES)", "COPY 1 %s"} {
				box.reached = nil
				command := fmt.Sprintf(form, name)
				tag := fmt.Sprintf("B%d", number)
				var responses []wireResponse
				if head, tail, literal := strings.Cut(command, "\r\n"); literal {
					write(t, c, tag+" "+head+"\r\n")
					if response := c.response(); response.tag != "+" {
						t.Fatalf("%s: %+v", form, response)
					}
					write(t, c, tail+"\r\n")
					responses = c.responsesThrough(tag)
				} else {
					responses = c.exchange(tag, "%s", command)
				}
				if last := responses[len(responses)-1]; last.kind != "OK" {
					t.Fatalf("%s: %+v", form, last)
				}
				if len(box.reached) == 0 {
					t.Fatalf("%s did not reach the store", form)
				}
				for _, reached := range box.reached {
					if reached != "INBOX" {
						t.Fatalf("%s reached the store as %q", form, reached)
					}
				}
				for _, response := range responses {
					if response.kind == "STATUS" && response.name != "INBOX" {
						t.Fatalf("STATUS named %q", response.name)
					}
				}
			}
			for _, command := range []string{"CREATE %s", "DELETE %s"} {
				if strings.Contains(name, "{") {
					continue
				}
				responses := c.exchange("D1", command, name)
				if last := responses[len(responses)-1]; last.kind != "NO" {
					t.Fatalf(command+": %+v", name, last)
				}
			}
			if strings.Contains(name, "{") {
				return
			}
			rows := c.exchange("L1", `LIST "" %s`, name)
			if len(rows) != 2 || rows[0].list == nil || rows[0].list.name != "INBOX" {
				t.Fatalf("LIST: %+v", rows)
			}
		})
	}
	t.Run("other names keep their case", func(t *testing.T) {
		box := &namedFolders{folders: []string{"INBOX", "Work"}}
		c := selectionClient(t, box)
		if responses := c.exchange("S1", "SELECT work"); responses[len(responses)-1].kind != "NO" {
			t.Fatalf("SELECT work: %+v", responses)
		}
		if responses := c.exchange("L1", `LIST "" work`); len(responses) != 1 {
			t.Fatalf("LIST work: %+v", responses)
		}
		if responses := c.exchange("S2", "SELECT Work"); responses[len(responses)-1].kind != "OK" {
			t.Fatalf("SELECT Work: %+v", responses)
		}
	})
	t.Run("APPEND", func(t *testing.T) {
		box := &namedFolders{folders: []string{"INBOX"}}
		c := selectionClient(t, box)
		write(t, c, "A1 APPEND iNbOx {3}\r\n")
		if response := c.response(); response.tag != "+" {
			t.Fatalf("continuation: %+v", response)
		}
		write(t, c, "abc\r\n")
		if responses := c.responsesThrough("A1"); responses[len(responses)-1].kind != "OK" {
			t.Fatalf("APPEND: %+v", responses)
		}
		if !slices.Contains(box.reached, "INBOX") {
			t.Fatalf("APPEND reached the store as %q", box.reached)
		}
	})
}

// TestListRows proves §9's mailbox-list for LIST and LSUB: a flag list, one
// quoted delimiter character, and a mailbox whose framing fits its name. Of
// \Noselect, \Marked and \Unmarked at most one appears. The patterns take "*"
// and "%", and every listed name that is not \Noselect can be selected by what
// the row says. A flat namespace is not offered, so the delimiter is never NIL.
//
// checklist: IMAP-INV-ABNF-17
func TestListRows(t *testing.T) {
	folders := []string{"INBOX", "Work", "Work/Q1", "Deep/Child", `Fancy "name"`, `Back\slash`, "Sp ace", "café", "Paren(s)"}
	for _, command := range []string{"LIST", "LSUB"} {
		t.Run(command, func(t *testing.T) {
			box := &namedFolders{folders: folders}
			c := selectionClient(t, box)
			for _, tc := range []struct {
				pattern string
				want    []string
			}{
				{`"*"`, []string{"INBOX", "Work", "Work/Q1", "Deep/Child", `Fancy "name"`, `Back\slash`, "Sp ace", "caf&AOk-", "Paren(s)"}},
				{`%`, []string{"INBOX", "Work", "Deep", `Fancy "name"`, `Back\slash`, "Sp ace", "caf&AOk-", "Paren(s)"}},
				{`Work/%`, []string{"Work/Q1"}},
				{`W*`, []string{"Work", "Work/Q1"}},
				{`"no such name"`, nil},
			} {
				responses := c.exchange("L1", `%s "" %s`, command, tc.pattern)
				var names []string
				for _, response := range responses[:len(responses)-1] {
					if response.kind != command || response.list == nil {
						t.Fatalf("%s: %+v", tc.pattern, response)
					}
					row := response.list
					names = append(names, row.name)
					if row.delimiter.isNil || row.delimiter.value != "/" {
						t.Fatalf("delimiter of %q: %s", row.name, row.delimiter)
					}
					exclusive := 0
					for _, attribute := range row.attributes {
						if slices.Contains([]string{`\Noselect`, `\Marked`, `\Unmarked`}, attribute) {
							exclusive++
						}
					}
					if exclusive > 1 {
						t.Fatalf("%q has %q", row.name, row.attributes)
					}
				}
				if last := responses[len(responses)-1]; last.kind != "OK" {
					t.Fatalf("%s: %+v", tc.pattern, last)
				}
				slices.Sort(names)
				want := slices.Clone(tc.want)
				slices.Sort(want)
				if !slices.Equal(names, want) {
					t.Fatalf("%s listed %q, want %q", tc.pattern, names, want)
				}
			}
		})
	}
	t.Run("root discovery", func(t *testing.T) {
		c := selectionClient(t, &namedFolders{folders: folders})
		responses := c.exchange("L1", `LIST "" ""`)
		if len(responses) != 2 || responses[0].list == nil || responses[0].list.name != "" || responses[0].list.delimiter.value != "/" {
			t.Fatalf("root: %+v", responses)
		}
		if !slices.Contains(responses[0].list.attributes, `\Noselect`) {
			t.Fatalf("root attributes: %q", responses[0].list.attributes)
		}
	})
	t.Run("a synthetic parent is Noselect", func(t *testing.T) {
		c := selectionClient(t, &namedFolders{folders: folders})
		for _, response := range c.exchange("L1", `LIST "" %%`) {
			if response.list != nil && response.list.name == "Deep" && !slices.Contains(response.list.attributes, `\Noselect`) {
				t.Fatalf("Deep: %q", response.list.attributes)
			}
		}
	})
}

// TestSearchReachesEveryMIMELeaf keeps the choice the engine made where §6.4.4
// leaves one: "A server implementation MAY exclude [MIME-IMB] body parts with
// terminal content media types other than TEXT and MESSAGE from consideration in
// SEARCH matching." The engine excludes none, whatever the leaf's media type and
// transfer encoding.
//
// checklist: IMAP-INV-SEARCH-01
func TestSearchReachesEveryMIMELeaf(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("needle"))
	for _, tc := range []struct{ name, part string }{
		{"text", "Content-Type: text/plain\r\n\r\nneedle\r\n"},
		{"application", "Content-Type: application/octet-stream\r\n\r\nneedle\r\n"},
		{"application base64", "Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\n" + encoded + "\r\n"},
		{"image base64", "Content-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n\r\n" + encoded + "\r\n"},
		{"audio", "Content-Type: audio/basic\r\n\r\nneedle\r\n"},
		{"message", "Content-Type: message/rfc822\r\n\r\nSubject: x\r\n\r\nneedle\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\n\r\nhay\r\n--b\r\n" + tc.part + "--b--\r\n"
			c := grammarFixture(t, raw)
			for _, key := range []string{"BODY needle", "TEXT needle"} {
				responses := c.exchange("S1", "SEARCH %s", key)
				if len(responses) != 2 || !slices.Equal(responses[0].numbers, []uint32{1}) {
					t.Fatalf("%s: %+v", key, responses)
				}
			}
			if responses := c.exchange("S2", "SEARCH BODY absent"); len(responses[0].numbers) != 0 {
				t.Fatalf("BODY absent: %+v", responses)
			}
		})
	}
}
