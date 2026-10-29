//go:build faultinject && fault_discover_wrong_media_type

package faultinject

// RESTMAIL discovery answers its JSON as text/plain.
func init() {
	register(Fault{
		Name:    "discover_wrong_media_type",
		Method:  "GET",
		Path:    "/.well-known/restmail",
		Break:   setMediaType("text/plain; charset=utf-8"),
		Example: `{"endpoint":"https://restmail.test/restmail"}`,
	})
}
