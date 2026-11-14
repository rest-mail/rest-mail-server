// Package timezone loads the IANA time zone names accounts store. It embeds the
// zone database, so a name works the same in a container image that has none.
package timezone

import (
	"fmt"
	"time"
	_ "time/tzdata"
)

// Load returns the location of an IANA time zone name. It refuses the two names
// time.LoadLocation accepts that name no zone: the empty string, which it reads
// as UTC, and "Local", the zone of whichever host is asking.
func Load(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("not an IANA time zone: %q", name)
	}
	return time.LoadLocation(name)
}
