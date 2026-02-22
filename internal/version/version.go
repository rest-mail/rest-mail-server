package version

// Set via -ldflags at build time:
//
//	go build -ldflags "-X github.com/restmail/restmail/internal/version.Version=1.0.0
//	                    -X github.com/restmail/restmail/internal/version.Commit=abc1234
//	                    -X github.com/restmail/restmail/internal/version.BuildDate=2026-02-22T12:00:00Z"
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)
