package apitypes

import (
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
)

// OpenExportSessionBody names what an export writes and how (#2116 AC03b).
// Each data has one format: mail as maildir, contacts as vcard, calendar as
// icalendar and settings as json.
type OpenExportSessionBody struct {
	Data   string `json:"data" enums:"mail,contacts,calendar,settings" message:"must be \"mail\", \"contacts\", \"calendar\" or \"settings\"" missing:"required: mail, contacts, calendar or settings" description:"What the export writes"`
	Format string `json:"format" enums:"maildir,vcard,icalendar,json" message:"must be \"maildir\", \"vcard\", \"icalendar\" or \"json\"" missing:"required: maildir, vcard, icalendar or json" description:"How the export writes it: mail as maildir, contacts as vcard, calendar as icalendar, settings as json"`
}

// AnyOf states the one format each data is exported in, so the description
// says which pairs are accepted and the binder refuses every other pair at
// format: no body the schema permits is refused for its pairing.
func (OpenExportSessionBody) AnyOf() oapifly.AnyOfRule {
	pair := func(data, format string) oapifly.Alternative {
		return oapifly.Alternative{Fields: map[string]string{"data": `enums:"` + data + `"`, "format": `enums:"` + format + `"`}}
	}
	return oapifly.AnyOfRule{
		Alternatives: []oapifly.Alternative{
			pair("mail", "maildir"), pair("contacts", "vcard"), pair("calendar", "icalendar"), pair("settings", "json"),
		},
		Field:   "format",
		Message: "must be maildir for mail, vcard for contacts, icalendar for calendar or json for settings",
	}
}

// ExportSession is an open export: close it with closeExportSession.
type ExportSession struct {
	ID             string    `json:"id" description:"Close the export with this id"`
	AccountID      uint      `json:"account_id"`
	Data           string    `json:"data"`
	Format         string    `json:"format"`
	StartedAt      time.Time `json:"started_at" format:"date-time"`
	TimeoutSeconds int       `json:"timeout_seconds" description:"An export not closed this long after it started is closed by the server as aborted"`
}

// CloseExportSessionBody is how an export ended, as the exporter reports it.
type CloseExportSessionBody struct {
	Result         string   `json:"result" enums:"completed,aborted" message:"must be \"completed\" or \"aborted\"" missing:"required: completed or aborted" description:"completed when every item asked for was written; aborted otherwise"`
	Rows           int64    `json:"rows" minimum:"0" message:"must not be negative" description:"Items written: messages, contacts, calendar parts, or 1 for a settings file"`
	Bytes          int64    `json:"bytes" minimum:"0" message:"must not be negative" description:"Bytes written"`
	SkippedFolders []string `json:"skipped_folders,omitempty" maxItems:"1000" description:"Folders the export was asked to leave out"`
}

// ExportSessionSummary is what a closed export wrote, as its audit entry records it.
type ExportSessionSummary struct {
	ID             string    `json:"id"`
	AccountID      uint      `json:"account_id"`
	Data           string    `json:"data"`
	Format         string    `json:"format"`
	Result         string    `json:"result" description:"completed or aborted"`
	Rows           int64     `json:"rows"`
	Bytes          int64     `json:"bytes"`
	SkippedFolders []string  `json:"skipped_folders"`
	StartedAt      time.Time `json:"started_at" format:"date-time"`
	EndedAt        time.Time `json:"ended_at" format:"date-time"`
}
