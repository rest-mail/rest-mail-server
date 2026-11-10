package mime

import (
	"bufio"
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"sync"
)

// A message can name a thousand parts of a few bytes each, and every one is opened, so what opening
// one costs is paid per part and not per byte: the library's base64 decoder is 1.8 KiB and its
// quoted-printable reader 4 KiB, however little they decode. Neither can be reset, so these read the
// same way with their room borrowed: it goes back when a part ends, and a part read by nobody simply
// leaves its room to the collector. TestPartDecodersReadAsTheLibraryDoes compares them.

// base64Room is what decoding a part needs while it's being read.
type base64Room struct {
	input  [1024]byte
	output [1024 / 4 * 3]byte
}

var base64Rooms = sync.Pool{New: func() any { return new(base64Room) }}

// base64Reader is encoding/base64's stream decoder for StdEncoding, with spaces and tabs skipped as
// line breaks are, and its buffers in a base64Room. This Read is a copy of that decoder's:
//
// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license, which is go/LICENSE in the Go source.
type base64Reader struct {
	source  io.Reader
	room    *base64Room
	nbuf    int
	out     []byte // leftover decoded output, in room
	err     error
	readErr error // from source
}

func newBase64Reader(source io.Reader) *base64Reader {
	return &base64Reader{source: source, room: base64Rooms.Get().(*base64Room)}
}

func (d *base64Reader) Read(p []byte) (n int, err error) {
	if len(d.out) > 0 {
		n = copy(p, d.out)
		d.out = d.out[n:]
		d.finished()
		return n, nil
	}
	if d.err != nil {
		return 0, d.err
	}
	for d.nbuf < 4 && d.readErr == nil {
		nn := min(max(len(p)/3*4, 4), len(d.room.input))
		nn, d.readErr = d.readEncoded(d.room.input[d.nbuf:nn])
		d.nbuf += nn
	}
	if d.nbuf < 4 {
		d.err = d.readErr
		if d.err == io.EOF && d.nbuf > 0 {
			d.err = io.ErrUnexpectedEOF
		}
		d.finished()
		return 0, d.err
	}
	nr := d.nbuf / 4 * 4
	nw := d.nbuf / 4 * 3
	if nw > len(p) {
		nw, d.err = base64.StdEncoding.Decode(d.room.output[:], d.room.input[:nr])
		d.out = d.room.output[:nw]
		n = copy(p, d.out)
		d.out = d.out[n:]
	} else {
		n, d.err = base64.StdEncoding.Decode(p, d.room.input[:nr])
	}
	d.nbuf -= nr
	copy(d.room.input[:d.nbuf], d.room.input[nr:])
	err = d.err
	d.finished()
	return n, err
}

// readEncoded reads what isn't a line break or a space, reading again past a run of nothing else.
func (d *base64Reader) readEncoded(p []byte) (int, error) {
	n, err := d.source.Read(p)
	for n > 0 {
		kept := 0
		for _, b := range p[:n] {
			if b != '\r' && b != '\n' && b != ' ' && b != '\t' {
				p[kept] = b
				kept++
			}
		}
		if kept > 0 {
			return kept, err
		}
		n, err = d.source.Read(p)
	}
	return n, err
}

// finished gives the room back once nothing more can come of it.
func (d *base64Reader) finished() {
	if d.err != nil && len(d.out) == 0 && d.room != nil {
		base64Rooms.Put(d.room)
		d.room, d.source = nil, nil
	}
}

var quotedBuffers = sync.Pool{New: func() any { return bufio.NewReader(nil) }}

// quotedReader is mime/quotedprintable's reader with its line buffer borrowed. That reader keeps a
// bufio.Reader it's given as its buffer.
type quotedReader struct {
	buffer  *bufio.Reader
	decoder *quotedprintable.Reader
	err     error
}

func newQuotedReader(source io.Reader) *quotedReader {
	buffer := quotedBuffers.Get().(*bufio.Reader)
	buffer.Reset(source)
	return &quotedReader{buffer: buffer, decoder: quotedprintable.NewReader(buffer)}
}

func (q *quotedReader) Read(p []byte) (int, error) {
	if q.err != nil {
		return 0, q.err
	}
	n, err := q.decoder.Read(p)
	if err != nil {
		q.err = err
		q.buffer.Reset(nil)
		quotedBuffers.Put(q.buffer)
		q.buffer, q.decoder = nil, nil
	}
	return n, err
}
