package rfctest

import "testing"

// RFC 1939 §7 PASS and Appendix A require the immediately preceding command
// to be a successful USER. Exercise the public dispatcher, not private handlers.
// checklist: POP3-PASS-05
func TestPassRequiresImmediateUser(t *testing.T) {
	for _, mode := range []string{"sequential", "pipelined"} {
		for _, tc := range []struct {
			name, command string
			replies       []string
		}{
			{"capa", "CAPA", []string{"+OK Capability list follows", "USER", "TOP", "UIDL", "RESP-CODES", "AUTH-RESP-CODE", "PIPELINING", "."}},
			{"unknown", "WHAT", []string{"-ERR Unknown command"}},
			{"transaction", "STAT", []string{"-ERR Not authenticated"}},
			{"malformed-user", "USER", []string{"-ERR Username required"}},
			{"empty-pass", "PASS", []string{"-ERR Password required"}},
			{"malformed-pass", "PASS\tsecret", []string{"-ERR Unknown command"}},
			{"refused-capa", "CAPA unexpected", []string{"-ERR Command accepts no arguments"}},
			{"refused-stls", "STLS", []string{"-ERR TLS not available"}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				b := &authPipelineBackend{}
				w := connect(t, b) // Memory/net.Pipe only; deadlines and joined Handle.
				commands := []string{"USER alice", tc.command, "PASS secret"}
				replies := append([]string{"+OK"}, tc.replies...)
				replies = append(replies, "-ERR USER first")
				if mode == "pipelined" {
					authPipeline(t, w, commands, replies)
				} else {
					expect(t, w.cmd(commands[0]), replies[0])
					expect(t, w.cmd(tc.command), tc.replies[0])
					for _, want := range tc.replies[1:] {
						expect(t, w.line(), want)
					}
					expect(t, w.cmd("PASS secret"), "-ERR USER first")
				}
				b.expectCalls(t) // No Authenticate, Messages, Retrieve, MarkSeen or Delete.
				expect(t, w.cmd("STAT"), "-ERR Not authenticated")
				expect(t, w.cmd("PASS secret"), "-ERR USER first")
				b.expectCalls(t)
				// A fresh immediate pair recovers, including a fully queued pair.
				if mode == "pipelined" {
					authPipeline(t, w, []string{"USER alice", "PASS secret", "STAT"},
						[]string{"+OK", "+OK Authentication successful", "+OK 1 42"})
				} else {
					w.login("secret")
					expect(t, w.cmd("STAT"), "+OK 1 42")
				}
				quit(t, w, "+OK")
				b.expectCalls(t, authPipelineCall{"authenticate", "alice", "secret"}, authPipelineCall{operation: "messages"})
			})
		}
	}
}
