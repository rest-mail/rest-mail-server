package outbound

import (
	"bufio"
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
)

const dkimHeaderPrefix = "DKIM-Signature: "

// SignDKIMReader consumes an RFC 5322 message and returns the folded
// DKIM-Signature field, including its name and final CRLF, ready to prepend to
// the original bytes. The caller owns the reader and can reopen or rewind it
// to publish the signed message. Body hashing uses fixed-size buffers.
func SignDKIMReader(raw io.Reader, opt dkim.SignOptions) (string, error) {
	algorithm, signOpts, err := streamDKIMAlgorithm(opt)
	if err != nil {
		return "", err
	}
	if opt.HeaderCanon == "" {
		opt.HeaderCanon = "relaxed"
	}
	if opt.BodyCanon == "" {
		opt.BodyCanon = "relaxed"
	}
	for _, canon := range []string{opt.HeaderCanon, opt.BodyCanon} {
		if canon != "simple" && canon != "relaxed" {
			return "", fmt.Errorf("dkim: unknown canonicalization %q", canon)
		}
	}

	r := bufio.NewReader(raw)
	headers, err := readDKIMHeaders(r)
	if err != nil {
		return "", fmt.Errorf("dkim: read headers: %w", err)
	}
	selection, err := streamDKIMHeaderList(headers, opt.Headers, opt.Oversign)
	if err != nil {
		return "", err
	}
	bodyHash, err := hashDKIMBody(r, opt.BodyCanon == "relaxed")
	if err != nil {
		return "", fmt.Errorf("dkim: read body: %w", err)
	}

	// The tag order and folding match go-dkim Sign. Its exported helper
	// selects actual field instances and canonicalizes them and the signature.
	fold := dkimSignatureFolder{column: len(dkimHeaderPrefix)}
	for _, tag := range []string{"v=1", "a=" + algorithm, "c=" + opt.HeaderCanon + "/" + opt.BodyCanon, "d=" + opt.Domain, "s=" + opt.Selector} {
		fold.tag(tag)
	}
	if opt.Time > 0 {
		fold.tag("t=" + strconv.FormatInt(opt.Time, 10))
	}
	if opt.Expiration > 0 {
		fold.tag("x=" + strconv.FormatInt(opt.Expiration, 10))
	}
	fold.tag("h=" + selection)
	fold.tag("bh=" + base64.StdEncoding.EncodeToString(bodyHash))
	fold.tag("b=")
	emptyB := fold.value.String()
	signature := dkim.Header{Name: "DKIM-Signature", Value: " " + emptyB, Raw: dkimHeaderPrefix + emptyB}
	data := dkim.BuildSignedHeaders(selection, headers, signature, opt.HeaderCanon)
	digest := sha256.Sum256([]byte(data))
	sig, err := opt.PrivateKey.Sign(rand.Reader, digest[:], signOpts)
	if err != nil {
		return "", fmt.Errorf("dkim: sign: %w", err)
	}
	fold.signature(base64.StdEncoding.EncodeToString(sig))
	return dkimHeaderPrefix + fold.value.String() + "\r\n", nil
}

func streamDKIMAlgorithm(opt dkim.SignOptions) (string, crypto.SignerOpts, error) {
	if opt.Domain == "" || opt.Selector == "" || opt.PrivateKey == nil {
		return "", nil, fmt.Errorf("dkim: domain, selector and private key are required")
	}
	if !validDKIMDNSName(opt.Domain) || !validDKIMDNSName(opt.Selector) {
		return "", nil, fmt.Errorf("dkim: domain %q and selector %q must be DNS names in A-label form", opt.Domain, opt.Selector)
	}
	const maxTimestamp = 999999999999 // RFC 6376 §3.5 allows 1*12DIGIT.
	if opt.Time > maxTimestamp || opt.Expiration > maxTimestamp || opt.Expiration < 0 ||
		(opt.Expiration > 0 && opt.Time > 0 && opt.Expiration <= opt.Time) {
		return "", nil, fmt.Errorf("dkim: invalid timestamp or expiration")
	}
	switch pub := opt.PrivateKey.Public().(type) {
	case *rsa.PublicKey:
		if pub.N.BitLen() < 1024 {
			return "", nil, fmt.Errorf("dkim: RSA key needs at least 1024 bits (RFC 8301 §3.2)")
		}
		return "rsa-sha256", crypto.SHA256, nil
	case ed25519.PublicKey:
		return "ed25519-sha256", crypto.Hash(0), nil // PureEdDSA over the digest (RFC 8463).
	default:
		return "", nil, fmt.Errorf("dkim: unsupported signing key type %T", pub)
	}
}

func validDKIMDNSName(name string) bool {
	if len(name) > 253 {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if strings.Trim(label, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
			return false
		}
	}
	return true
}

// readDKIMHeaders isolates the header block for go-dkim's parser and leaves
// the same buffered reader at the body. As with SplitMessage, bare CR and LF
// are line breaks and EOF without a separator denotes a message with no body.
func readDKIMHeaders(r *bufio.Reader) ([]dkim.Header, error) {
	var head bytes.Buffer
	lineEmpty := true
	for {
		c, err := r.ReadByte()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if c != '\r' && c != '\n' {
			head.WriteByte(c)
			lineEmpty = false
			continue
		}
		if c == '\r' {
			next, err := r.Peek(1)
			if err != nil && err != io.EOF {
				return nil, err
			}
			if len(next) > 0 && next[0] == '\n' {
				_, _ = r.Discard(1)
			}
		}
		if lineEmpty {
			break
		}
		head.WriteString("\r\n")
		lineEmpty = true
	}
	headers, _ := dkim.SplitMessage(head.Bytes())
	return headers, nil
}

func hashDKIMBody(r *bufio.Reader, relaxed bool) ([]byte, error) {
	digest := sha256.New()
	// Writes and flushing cannot fail because the underlying writer is a hash.
	out := bufio.NewWriter(digest)
	var lineBreaks int64
	var afterCR, pendingSpace, hasContent bool
	for {
		c, err := r.ReadByte()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if afterCR && c == '\n' {
			afterCR = false
			continue
		}
		afterCR = c == '\r'
		if c == '\r' || c == '\n' {
			lineBreaks++
			pendingSpace = false
			continue
		}
		if relaxed && (c == ' ' || c == '\t') {
			pendingSpace = true
			continue
		}
		// Only later content can confirm that these line breaks and spaces
		// belong in the hash. The pending state stores no bytes from the body.
		for ; lineBreaks > 0; lineBreaks-- {
			_, _ = out.WriteString("\r\n")
		}
		if pendingSpace {
			_ = out.WriteByte(' ')
			pendingSpace = false
		}
		_ = out.WriteByte(c)
		hasContent = true
	}
	if hasContent || !relaxed {
		_, _ = out.WriteString("\r\n")
	}
	_ = out.Flush()
	return digest.Sum(nil), nil
}

// streamDKIMHeaderList mirrors go-dkim's unexported h= construction. Each
// selected name appears once per occurrence, plus one for oversigned names.
func streamDKIMHeaderList(headers []dkim.Header, want, oversign []string) (string, error) {
	if len(want) == 0 && len(oversign) == 0 {
		want, oversign = dkim.DefaultSignedHeaders, dkim.DefaultOversignedHeaders
	}
	counts := make(map[string]int)
	for _, h := range headers {
		counts[strings.ToLower(h.Name)]++
	}
	var order []string
	extra := make(map[string]int)
	for more, names := range [][]string{want, oversign} {
		for _, name := range names {
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" || strings.ContainsFunc(name, func(c rune) bool { return c < 33 || c > 126 || c == ':' }) {
				return "", fmt.Errorf("dkim: invalid header field name %q", name)
			}
			if _, seen := extra[name]; !seen {
				order = append(order, name)
			}
			extra[name] = more
		}
	}
	if _, selected := extra["from"]; !selected || counts["from"] == 0 {
		return "", fmt.Errorf("dkim: a From header must be present and selected (RFC 6376 §5.4)")
	}
	var list []string
	for _, name := range order {
		for range counts[name] + extra[name] {
			list = append(list, name)
		}
	}
	return strings.Join(list, ":"), nil
}

// dkimSignatureFolder mirrors go-dkim's unexported signature folding: fold
// between tags, after colons in a long h= list, and inside the base64 b= value.
type dkimSignatureFolder struct {
	value  strings.Builder
	column int
}

const dkimLineLimit = 78

func (f *dkimSignatureFolder) write(s string) {
	f.value.WriteString(s)
	f.column += len(s)
}

func (f *dkimSignatureFolder) fold() {
	f.value.WriteString("\r\n ")
	f.column = 1
}

func (f *dkimSignatureFolder) tag(tag string) {
	if f.value.Len() > 0 {
		f.write(";")
		if f.column+1+len(tag) > dkimLineLimit {
			f.fold()
		} else {
			f.write(" ")
		}
	}
	list, isHeaderList := strings.CutPrefix(tag, "h=")
	if !isHeaderList || f.column+len(tag) <= dkimLineLimit {
		f.write(tag)
		return
	}
	f.write("h=")
	for i, field := range strings.Split(list, ":") {
		if i > 0 {
			f.write(":")
		}
		if f.column+len(field)+1 > dkimLineLimit {
			f.fold()
		}
		f.write(field)
	}
}

func (f *dkimSignatureFolder) signature(s string) {
	for s != "" {
		if f.column >= dkimLineLimit {
			f.fold()
		}
		n := min(len(s), dkimLineLimit-f.column)
		f.write(s[:n])
		s = s[n:]
	}
}
