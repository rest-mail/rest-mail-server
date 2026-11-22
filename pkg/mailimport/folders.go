package mailimport

import rmail "github.com/rest-mail/rest-mail-server/internal/mail"

// Destinations remembers, for one run, which folder the API stored each supplied folder's mail
// in. A name the API had to convert may have gone to a numbered folder, and the rest of that
// source folder has to follow it there.
type Destinations struct {
	stored map[string]string
}

// NewDestinations returns the memory for one import run.
func NewDestinations() *Destinations {
	return &Destinations{stored: map[string]string{}}
}

// For is the folder to send a message from supplied to.
func (d *Destinations) For(supplied string) string {
	if stored, ok := d.stored[supplied]; ok {
		return stored
	}
	return supplied
}

// Record notes the folder the API answered with for supplied. It reports false for an answer the
// API's rule cannot give: only a converted name may come back numbered.
func (d *Destinations) Record(supplied, stored string) bool {
	canonical := rmail.CanonicalFolder(supplied)
	if stored != canonical && (rmail.NormalizeFolderName(supplied) == supplied || !rmail.IsNumberedFolder(stored, canonical)) {
		return false
	}
	d.stored[supplied] = stored
	return true
}
