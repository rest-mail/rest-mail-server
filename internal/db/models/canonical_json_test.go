package models

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"testing"
)

// What isCanonicalJSON takes as it is, the long way would have written the same.
func TestCanonicalJSONMatchesAReencoding(t *testing.T) {
	reencoded := func(data []byte) ([]byte, bool) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil || decoder.More() {
			return nil, false
		}
		out, err := json.Marshal(value)
		return out, err == nil
	}
	check := func(data []byte) {
		t.Helper()
		if !isCanonicalJSON(data) {
			return
		}
		out, ok := reencoded(data)
		if !ok || !bytes.Equal(out, data) {
			t.Fatalf("%q taken as it is, reencodes as %q (%v)", data, out, ok)
		}
	}
	seeds := []string{
		`[]`, `{}`, `["a"]`, `["a","b"]`, `{"a":["b"]}`, `{"a":["b","c"],"b":[]}`, `{"b":1,"a":2}`, `{"a":1,"a":2}`, `{"a":"x","a":"y"}`, `{"a":"x","b":"y"}`,
		`"\"\\\b\f\n\r\t"`, `"\u003c\u003e\u0026"`, `"\u0000\u001f\u2028\u2029\ufffd"`, `"<>&"`, "\"\xff\"", "\"\xc3\xa9\xef\xbf\xbd\"", "\"\xe2\x80\xa8\"", `" "`, `"/"`, `"\/"`, `null`, `true`, `1`, `1.0`, ` []`, `[] `, `[ ]`, `{"a": []}`, `[[[["a"]]]]`,
		`{"Z":[],"a":[]}`, `{"\u0041":[],"B":[]}`, `{"a\"":[],"a":[]}`, `{"":[]}`, "\"\x7f\"",
	}
	for _, seed := range seeds {
		check([]byte(seed))
	}
	for _, want := range []string{`[]`, `{"a":["b","c"],"b":[]}`, `"\"\\\b\f\n\r\t"`, `"\u003c\u003e\u0026"`, "\"\xc3\xa9\"", "\"\x7f\""} {
		if !isCanonicalJSON([]byte(want)) {
			t.Fatalf("%q should be taken as it is", want)
		}
		if out, ok := reencoded([]byte(want)); !ok || string(out) != want {
			t.Fatalf("%q reencodes as %q", want, out)
		}
	}
	random := rand.New(rand.NewSource(1915))
	alphabet := []byte(`[]{}:,"\ubfnrt0a3cABZ<>&` + "\x00\x1f\x7f\xc3\xa9\xff")
	for range 300000 {
		data := []byte(seeds[random.Intn(len(seeds))])
		for range 1 + random.Intn(3) {
			if len(data) == 0 {
				break
			}
			at := random.Intn(len(data))
			c := alphabet[random.Intn(len(alphabet))]
			switch random.Intn(3) {
			case 0:
				data = append(data[:at:at], append([]byte{c}, data[at:]...)...)
			case 1:
				data = append(data[:at:at], data[at+1:]...)
			default:
				data[at] = c
			}
		}
		check(data)
	}
}
