package messagestore

import (
	"encoding/hex"
	"errors"
	"hash"
	"io"
)

type verifiedSource struct {
	body     io.Closer
	reader   io.Reader
	hash     hash.Hash
	left     int64
	checksum string
	end      error
}

func (r *verifiedSource) Read(p []byte) (int, error) {
	if r.end != nil {
		return 0, r.end
	}
	if len(p) == 0 {
		return 0, nil
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	var n int
	var err error
	if len(p) > 0 {
		n, err = r.reader.Read(p)
		_, _ = r.hash.Write(p[:n])
		r.left -= int64(n)
	}
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF) && r.left > 0:
		r.end = ErrCorruptSource
	case err != nil && !errors.Is(err, io.EOF):
		r.end = err
	case r.left == 0:
		r.end = r.verifyEnd(err)
	}
	if r.end != nil && (n == 0 || r.end != io.EOF) {
		// Withhold the final bytes on failure so io.ReadFull and exact-size
		// copies cannot discard an integrity error on the final read.
		return 0, r.end
	}
	return n, nil
}

func (r *verifiedSource) verifyEnd(readErr error) error {
	if hex.EncodeToString(r.hash.Sum(nil)) != r.checksum {
		return ErrCorruptSource
	}
	if readErr == nil {
		var extra [1]byte
		_, err := io.ReadFull(r.reader, extra[:])
		switch err {
		case nil:
			return ErrCorruptSource
		case io.EOF:
		default:
			return err
		}
	}
	return io.EOF
}

func (r *verifiedSource) Close() error { return r.body.Close() }
