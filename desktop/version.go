package main

// Set at build time through -ldflags by the chore desktop tasks and the release workflow.
var (
	version = "dev"
	commit  = "unknown"
	builtAt = "unknown"
)

// AppInfo tells the frontend which build is running.
type AppInfo struct{}

// Release is the version, commit and build time of this binary.
type Release struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"builtAt"`
}

// Release returns this binary's release information.
func (AppInfo) Release() Release {
	return Release{Version: version, Commit: commit, BuiltAt: builtAt}
}
