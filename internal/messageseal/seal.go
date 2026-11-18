package messageseal

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/boundedpool"
)

// digestBuffer is a struct, not a bare array: with a pool of *[N]byte, slicing
// the pooled pointer crashes the Go 1.26.10 compiler under -cover ("bad ptr to
// array in slice go.shape.*uint8"), and the topology's API is built with -cover.
type digestBuffer struct{ bytes [32 << 10]byte }

var digestBuffers = boundedpool.New(16, func() *digestBuffer { return new(digestBuffer) })

func Digest(source io.Reader, fields ...string) (string, error) {
	room := digestBuffers.Get()
	defer digestBuffers.Put(room)
	buffer := room.bytes[:]
	raw := sha256.New()
	if _, err := io.CopyBuffer(raw, struct{ io.Reader }{source}, buffer); err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("restmail-seal-v1\x00"))
	_, _ = digest.Write(raw.Sum(nil))
	var size [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = digest.Write(size[:])
		if _, err := io.CopyBuffer(digest, struct{ io.Reader }{strings.NewReader(field)}, buffer); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
