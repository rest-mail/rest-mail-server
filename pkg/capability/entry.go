// Package capability holds the wire form of the capabilities an account holds,
// as the REST Mail API reports them (for example in a login response's
// scoped_capabilities). It imports nothing from the server, so a client can use
// it without linking server packages.
package capability

// Entry is one capability held at one scope.
type Entry struct {
	Scope      string `json:"scope"`
	Capability string `json:"capability"`
}
