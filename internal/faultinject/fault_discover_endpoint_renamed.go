//go:build faultinject && fault_discover_endpoint_renamed

package faultinject

// RESTMAIL discovery answers with its required endpoint field renamed.
func init() {
	register(Fault{
		Name:    "discover_endpoint_renamed",
		Method:  "GET",
		Path:    "/.well-known/restmail",
		Break:   renameField("endpoint", "url"),
		Example: `{"endpoint":"https://restmail.test/restmail"}`,
	})
}
