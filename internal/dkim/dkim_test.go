package dkim

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

func TestRecordName(t *testing.T) {
	if got := RecordName("default", "mail3.test"); got != "default._domainkey.mail3.test" {
		t.Errorf("RecordName = %q", got)
	}
	if got := RecordName("", "acme.example"); got != "default._domainkey.acme.example" {
		t.Errorf("empty selector should default: %q", got)
	}
}

func TestGenerateKeyAndRecordRoundTrip(t *testing.T) {
	priv, pub, err := GenerateKey(1024) // small for test speed; validity is what matters
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Private PEM must parse as an RSA key.
	pb, _ := pem.Decode([]byte(priv))
	if pb == nil || pb.Type != "RSA PRIVATE KEY" {
		t.Fatalf("bad private PEM block: %+v", pb)
	}
	privKey, err := x509.ParsePKCS1PrivateKey(pb.Bytes)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}

	rec, err := RecordValue(pub)
	if err != nil {
		t.Fatalf("RecordValue: %v", err)
	}
	if !strings.HasPrefix(rec, "v=DKIM1; k=rsa; p=") {
		t.Fatalf("record value has wrong prefix: %q", rec)
	}

	// The p= payload must decode to the public half of the generated key.
	b64 := strings.TrimPrefix(rec, "v=DKIM1; k=rsa; p=")
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("p= is not valid base64: %v", err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatalf("p= is not a valid public key: %v", err)
	}
	rsaPub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatal("p= is not an RSA public key")
	}
	if rsaPub.N.Cmp(privKey.N) != 0 || rsaPub.E != privKey.E {
		t.Error("record public key does not match generated private key")
	}
}

func TestRecordFragmentChunks(t *testing.T) {
	// Short value → single quoted string.
	short := RecordFragment("default._domainkey.d.test", "v=DKIM1; k=rsa; p=abc")
	if short != `txt-record=default._domainkey.d.test,"v=DKIM1; k=rsa; p=abc"` {
		t.Errorf("short fragment wrong: %s", short)
	}

	// A value longer than 255 chars must split into multiple <=255 strings
	// whose concatenation reproduces the original.
	long := "v=DKIM1; k=rsa; p=" + strings.Repeat("A", 400)
	frag := RecordFragment("n", long)
	quoted := frag[len("txt-record=n,"):]
	parts := strings.Split(quoted, ",")
	if len(parts) < 2 {
		t.Fatalf("expected multiple chunks, got %d: %s", len(parts), frag)
	}
	var joined string
	for _, p := range parts {
		s := strings.TrimPrefix(strings.TrimSuffix(p, `"`), `"`)
		if len(s) > 255 {
			t.Errorf("chunk exceeds 255 chars: %d", len(s))
		}
		joined += s
	}
	if joined != long {
		t.Error("chunks do not reassemble to the original value")
	}
}

func TestRecordValueRejectsBadInput(t *testing.T) {
	if _, err := RecordValue("not a pem"); err == nil {
		t.Error("expected error for non-PEM input")
	}
	// A valid PEM block that isn't a public key.
	junk := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("junk")}))
	if _, err := RecordValue(junk); err == nil {
		t.Error("expected error for non-key PEM")
	}
}
