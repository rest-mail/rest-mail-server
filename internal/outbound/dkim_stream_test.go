package outbound

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
)

const streamDKIMTestHead = "From: alice@example.test\r\n" +
	"SUBJECT: Grüße with\t space \t\r\n\tand a fold\r\n" +
	"To: bob@example.test\r\nTO: carol@example.test\r\n" +
	"List-Unsubscribe: <https://example.test/unsubscribe>\r\n" +
	"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n" +
	"invalid header\r\n\tignored fold\r\nMIME-Version: 1.0\r\n"

func TestSignDKIMReaderMatchesAndVerifies(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	edKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	messages := []struct{ name, raw string }{
		{"empty body", streamDKIMTestHead + "\r\n"},
		{"header only", strings.TrimSuffix(streamDKIMTestHead, "\r\n")},
		{"empty lines", streamDKIMTestHead + "\r\n\r\n\r\n"},
		{"whitespace only", streamDKIMTestHead + "\r\n \t \r\n\t\r\n"},
		{"interior and trailing lines", streamDKIMTestHead + "\r\n\r\n\t Grieß \t  one \t\r\n\r\n \t\r\n  two\r\n\r\n\r\n"},
		{"body without final CRLF", streamDKIMTestHead + "\r\nbody\t "},
		{"mixed line endings", "From: alice@example.test\rSubject: mixed\n\tfold\r\n\rfirst\rsecond\n\r\nthird\r\r\n"},
		{"bare LF", "From: alice@example.test\n\nfirst\nsecond\n\n"},
		{"bare final CR", streamDKIMTestHead + "\r\n\ttext\r"},
	}

	for _, key := range []struct {
		name   string
		signer crypto.Signer
		der    []byte
	}{
		{"rsa", rsaKey, x509.MarshalPKCS1PublicKey(&rsaKey.PublicKey)},
		{"ed25519", edKey, edKey.Public().(ed25519.PublicKey)},
	} {
		resolver := func(_ context.Context, name string) ([]string, error) {
			if name != "sel._domainkey.example.test" {
				t.Fatalf("unexpected DNS name %q", name)
			}
			return []string{"v=DKIM1; k=" + key.name + "; p=" + base64.StdEncoding.EncodeToString(key.der)}, nil
		}
		for _, canon := range []struct{ name, header, body string }{
			{"defaults", "", ""},
			{"relaxed-simple", "relaxed", "simple"},
			{"simple-relaxed", "simple", "relaxed"},
			{"simple-simple", "simple", "simple"},
		} {
			for _, msg := range messages {
				t.Run(key.name+"/"+canon.name+"/"+msg.name, func(t *testing.T) {
					opt := dkim.SignOptions{
						Domain: "example.test", Selector: "sel", PrivateKey: key.signer,
						Time: 1700000000, HeaderCanon: canon.header, BodyCanon: canon.body,
					}
					if canon.name != "defaults" {
						opt.Headers = append([]string{"  fROM ", "from"}, signedHeaders...)
						opt.Oversign = append([]string{"x-absent", "TO", "to"}, dkim.DefaultOversignedHeaders...)
						opt.Expiration = 900000000000
					}
					want, err := dkim.Sign([]byte(msg.raw), opt)
					if err != nil {
						t.Fatal(err)
					}
					got, err := SignDKIMReader(iotest.OneByteReader(strings.NewReader(msg.raw)), opt)
					if err != nil {
						t.Fatal(err)
					}
					if want := "DKIM-Signature: " + want + "\r\n"; got != want {
						t.Fatalf("signature differs\n got: %q\nwant: %q", got, want)
					}
					result := dkim.Verify(t.Context(), []byte(got+msg.raw), resolver)
					if len(result) != 1 || result[0].Result != dkim.ResultPass {
						t.Fatalf("verification result: %+v", result)
					}
				})
			}
		}
	}
}

func TestSignDKIMReaderReadErrors(t *testing.T) {
	failure := errors.New("source failed")
	opt := dkim.SignOptions{Domain: "example.test", Selector: "sel", PrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
	for _, prefix := range []string{"From:", streamDKIMTestHead + "\r", streamDKIMTestHead + "\r\nbody"} {
		raw := io.MultiReader(strings.NewReader(prefix), iotest.ErrReader(failure))
		if got, err := SignDKIMReader(raw, opt); got != "" || !errors.Is(err, failure) {
			t.Errorf("got signature %q and error %v, want %v", got, err, failure)
		}
	}
}

type repeatDKIMByte byte

func (c repeatDKIMByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(c)
	}
	return len(p), nil
}

func TestSignDKIMReaderBoundsBodyAllocation(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	for _, canon := range []string{"simple", "relaxed"} {
		t.Run(canon, func(t *testing.T) {
			opt := dkim.SignOptions{Domain: "example.test", Selector: "sel", PrivateKey: key, BodyCanon: canon}
			for _, size := range []int64{1 << 20, 32 << 20} {
				// Generate long lines, a long whitespace run, and long empty-line
				// runs both between content and at EOF without allocating them.
				raw := io.MultiReader(strings.NewReader(streamDKIMTestHead+"\r\n"),
					io.LimitReader(repeatDKIMByte('x'), size/4),
					io.LimitReader(repeatDKIMByte('\t'), size/4),
					io.LimitReader(repeatDKIMByte('\n'), size/4),
					strings.NewReader("z"),
					io.LimitReader(repeatDKIMByte('\n'), size/4-1),
				)
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				got, err := SignDKIMReader(raw, opt)
				runtime.ReadMemStats(&after)
				if err != nil || got == "" {
					t.Fatalf("signature missing: %v", err)
				}
				allocated := after.TotalAlloc - before.TotalAlloc
				t.Logf("%d body bytes allocated %d bytes", size, allocated)
				if allocated > 1<<20 {
					t.Fatalf("signing allocated %d bytes, want at most 1 MiB", allocated)
				}
			}
		})
	}
}
