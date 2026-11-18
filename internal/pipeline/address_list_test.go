package pipeline

import (
	"encoding/json"
	"testing"
)

func TestAddressListJSONPreservesArrays(t *testing.T) {
	for _, body := range []string{"null", "[]", `[{"name":"Sender","address":"sender@example.test"}]`} {
		t.Run(body, func(t *testing.T) {
			var addresses AddressList
			if err := json.Unmarshal([]byte(body), &addresses); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(addresses)
			if err != nil {
				t.Fatal(err)
			}
			want := body
			if body == "null" {
				want = "[]"
			}
			if string(encoded) != want {
				t.Fatalf("address array = %s, want %s", encoded, want)
			}
		})
	}
}
