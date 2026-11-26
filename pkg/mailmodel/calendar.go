package mailmodel

import "time"

// CalendarEvent represents a parsed VEVENT from an iCalendar (text/calendar) MIME part.
type CalendarEvent struct {
	Method      string            `json:"method"`                // REQUEST, REPLY, CANCEL, etc.
	UID         string            `json:"uid"`                   // Unique event identifier
	Summary     string            `json:"summary"`               // Event title
	Description string            `json:"description,omitempty"` // Event description
	Location    string            `json:"location,omitempty"`    // Event location
	DTStart     time.Time         `json:"dtstart"`               // Start time
	DTEnd       time.Time         `json:"dtend"`                 // End time
	AllDay      bool              `json:"all_day"`               // True if this is an all-day event
	Organizer   CalendarAddress   `json:"organizer"`             // Event organizer
	Attendees   []CalendarAddress `json:"attendees,omitempty"`   // Event attendees
	Status      string            `json:"status,omitempty"`      // CONFIRMED, TENTATIVE, CANCELLED
	Sequence    int               `json:"sequence"`              // Update sequence number
	DTStamp     time.Time         `json:"dtstamp,omitempty"`     // Timestamp of the calendar object
}

// CalendarAddress represents an organizer or attendee with optional metadata.
type CalendarAddress struct {
	Address  string `json:"address"`            // Email address (stripped of mailto:)
	Name     string `json:"name,omitempty"`     // Display name (CN= parameter)
	Role     string `json:"role,omitempty"`     // REQ-PARTICIPANT, OPT-PARTICIPANT, etc.
	PartStat string `json:"partstat,omitempty"` // NEEDS-ACTION, ACCEPTED, DECLINED, TENTATIVE
	RSVP     bool   `json:"rsvp,omitempty"`     // Whether RSVP is requested
}
