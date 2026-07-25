package filters

// Shallow-copy safety for transform filters (issue #201).
//
// A transform filter builds its working copy as `modified := *email`. That is a
// shallow struct copy: the map fields (Headers.Raw, Headers.Extra, Metadata) and
// their slice values are SHARED with the caller's original. Mutating them on the
// copy (delete/append/overwrite) therefore also mutates the caller's message.
// These helpers clone the maps a filter is about to mutate so the original is
// left untouched.

// cloneRawHeaders returns a deep copy of a Raw header map (each []string value is
// copied too), or a fresh empty map when the input is nil, so the caller can
// append/delete without aliasing the original.
func cloneRawHeaders(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in)+1)
	for k, v := range in {
		cp := make([]string, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// cloneStringMap returns a shallow copy of a string map, or a fresh empty map
// when the input is nil.
func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}
