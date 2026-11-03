package mail

import (
	"reflect"
	"testing"
)

// The targets a List-Unsubscribe field offers (#1575): one-click only with the RFC 8058
// List-Unsubscribe-Post value, other HTTPS URIs as links, mailto: with its subject and body,
// and nothing from an item that does not parse.
func TestParseUnsubscribe(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string][]string
		want    UnsubscribeTargets
	}{
		{
			name: "one-click",
			headers: map[string][]string{
				"List-Unsubscribe":      {"<https://news.example/u?t=abc>, <mailto:leave@news.example?subject=unsubscribe>"},
				"List-Unsubscribe-Post": {"List-Unsubscribe=One-Click"},
			},
			want: UnsubscribeTargets{
				OneClick: "https://news.example/u?t=abc",
				Mailto:   []UnsubscribeMailto{{To: "leave@news.example", Subject: "unsubscribe"}},
			},
		},
		{
			name:    "a plain link without List-Unsubscribe-Post is not one-click",
			headers: map[string][]string{"List-Unsubscribe": {"<https://news.example/u?t=abc>"}},
			want:    UnsubscribeTargets{Links: []string{"https://news.example/u?t=abc"}},
		},
		{
			name: "another List-Unsubscribe-Post value is not one-click",
			headers: map[string][]string{
				"List-Unsubscribe":      {"<https://news.example/u>"},
				"List-Unsubscribe-Post": {"List-Unsubscribe=Yes"},
			},
			want: UnsubscribeTargets{Links: []string{"https://news.example/u"}},
		},
		{
			name:    "mailto with an escaped subject and body, a plus kept as a plus",
			headers: map[string][]string{"List-Unsubscribe": {"<mailto:list-request@host.example?subject=remove%20me&body=a+b%0Ac&to=extra@evil.example>"}},
			want:    UnsubscribeTargets{Mailto: []UnsubscribeMailto{{To: "list-request@host.example", Subject: "remove me", Body: "a+b\nc"}}},
		},
		{
			name: "malformed items are left out",
			headers: map[string][]string{"List-Unsubscribe": {
				"<javascript:alert(1)>, <http://plain.example/u>, <https://user:pw@news.example/u>, " +
					"<mailto:a@x.example,b@y.example>, <mailto:Name <x@y.example>>, <mailto:x@y.example?subject=a%0D%0ABcc:%20z@evil.example>, not-a-uri"}},
			want: UnsubscribeTargets{},
		},
		{
			name: "a field given twice is not read",
			headers: map[string][]string{"List-Unsubscribe": {"<https://a.example/u>", "<https://b.example/u>"},
				"List-Unsubscribe-Post": {"List-Unsubscribe=One-Click"}},
			want: UnsubscribeTargets{},
		},
		{
			name:    "no field",
			headers: map[string][]string{"Subject": {"hello"}},
			want:    UnsubscribeTargets{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseUnsubscribe(tc.headers)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseUnsubscribe() = %+v, want %+v", got, tc.want)
			}
			if got.Empty() != tc.want.Empty() {
				t.Fatalf("Empty() = %v", got.Empty())
			}
		})
	}
}
