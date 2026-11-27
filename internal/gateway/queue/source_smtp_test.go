package queue

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"net/textproto"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/messagestore"
)

type sourceReadFunc func([]byte) (int, error)

func (r sourceReadFunc) Read(p []byte) (int, error) { return r(p) }

func TestSourceDATAFailureClosesBeforeTerminator(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
		end  func(context.Context) error
	}{
		{"truncated", messagestore.ErrCorruptSource, func(context.Context) error { return io.EOF }},
		{"corrupt", messagestore.ErrCorruptSource, func(context.Context) error { return messagestore.ErrCorruptSource }},
		{"stalled", context.DeadlineExceeded, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
	} {
		t.Run(failure.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			first := strings.NewReader(strings.Repeat(".test!\r\n", dataBlockSize/8))
			raw := sourceReadFunc(func(p []byte) (int, error) {
				if first.Len() != 0 {
					return first.Read(p)
				}
				return 0, failure.end(ctx)
			})
			source := &smtpSource{ctx: ctx, cancel: cancel, raw: io.NopCloser(raw), size: dataBlockSize + 1, timeout: 250 * time.Millisecond}
			conn, peer := net.Pipe()
			defer conn.Close()
			defer peer.Close()
			_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
			transcript := make(chan error, 1)
			go func() { transcript <- readAbortedSourceDATA(peer) }()
			client, err := smtp.NewClient(conn, "peer.test")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			start := time.Now()
			err = sendOverSMTPLink(&smtpLink{conn: conn, client: client}, config.OutboundSMTPTimeouts{}.WithDefaults(),
				"peer.test", "sender@example.test", "recipient@example.test", smtpContent{source: source}, false)
			if !errors.Is(err, failure.err) {
				t.Fatalf("delivery returned %v, want %v", err, failure.err)
			}
			if err := <-transcript; err != nil {
				t.Fatal(err)
			}
			t.Logf("closed after %v with the peer inside DATA", time.Since(start))
		})
	}
}

func readAbortedSourceDATA(conn net.Conn) error {
	reader := bufio.NewReader(conn)
	reply := "220 peer ready\r\n"
	for _, command := range []string{"EHLO ", "MAIL ", "RCPT ", "DATA\r\n"} {
		if _, err := io.WriteString(conn, reply); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, command) {
			return fmt.Errorf("want %q, got %q (%v)", command, line, err)
		}
		reply = "250 ok\r\n"
	}
	if _, err := io.WriteString(conn, "354 continue\r\n"); err != nil {
		return err
	}
	var lines int
	for {
		line, err := reader.ReadSlice('\n')
		if err == io.EOF && lines > 0 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("DATA ended after %d lines: %w", lines, err)
		}
		if string(line) == ".\r\n" {
			return errors.New("peer received a DATA terminator for a partial message")
		}
		lines++
	}
}

func TestStreamingSourceDATAAllocation(t *testing.T) {
	const size = 32 << 20
	const allocationCeiling = 4 << 20
	block := []byte(strings.Repeat(".test!\r\n", dataBlockSize/8))
	remaining := size
	raw := sourceReadFunc(func(p []byte) (int, error) {
		if remaining == 0 {
			return 0, io.EOF
		}
		n := copy(p, block[:min(remaining, len(block))])
		remaining -= n
		return n, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	source := &smtpSource{ctx: ctx, cancel: cancel, raw: io.NopCloser(raw), size: size, timeout: time.Second}
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	out := bufio.NewWriter(io.Discard)
	data := textproto.NewWriter(out).DotWriter()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := source.write(data, out.Flush, conn, "peer.test"); err != nil {
		t.Fatal(err)
	}
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("allocated %d bytes while sending %d source bytes", allocated, size)
	if remaining != 0 || allocated >= allocationCeiling {
		t.Fatalf("bytes remaining=%d allocated=%d, want none and under %d allocated", remaining, allocated, allocationCeiling)
	}
}
