package handlers

import (
	"encoding/json"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
)

// Sample emails may be partial: the engine supplies zero values for omitted fields, as
// encoding/json did before typed registration. Pointer fields also preserve acceptance of
// explicit nulls. Every canonical email field sent by the clients is declared here.
type sampleEmail struct {
	Envelope       *sampleEnvelope        `json:"envelope,omitempty"`
	Headers        *sampleHeaders         `json:"headers,omitempty"`
	Body           *sampleBody            `json:"body,omitempty"`
	Attachments    *[]sampleAttachment    `json:"attachments,omitempty"`
	Inline         *[]sampleAttachment    `json:"inline,omitempty"`
	CalendarEvents *[]sampleCalendarEvent `json:"calendar_events,omitempty"`
	Metadata       *map[string]string     `json:"metadata,omitempty"`
}
type sampleEnvelope struct {
	MailFrom       *string   `json:"mail_from,omitempty"`
	RcptTo         *[]string `json:"rcpt_to,omitempty"`
	ClientIP       *string   `json:"client_ip,omitempty"`
	ClientHostname *string   `json:"client_hostname,omitempty"`
	Helo           *string   `json:"helo,omitempty"`
	TLS            *bool     `json:"tls,omitempty"`
	Direction      *string   `json:"direction,omitempty"`
}
type sampleHeaders struct {
	From       *[]sampleAddress     `json:"from,omitempty"`
	To         *[]sampleAddress     `json:"to,omitempty"`
	Cc         *[]sampleAddress     `json:"cc,omitempty"`
	Bcc        *[]sampleAddress     `json:"bcc,omitempty"`
	Subject    *string              `json:"subject,omitempty"`
	Date       *string              `json:"date,omitempty"`
	MessageID  *string              `json:"message-id,omitempty"`
	InReplyTo  *string              `json:"in-reply-to,omitempty"`
	References *[]string            `json:"references,omitempty"`
	Raw        *map[string][]string `json:"raw,omitempty"`
	Extra      *map[string]string   `json:"extra,omitempty"`
}
type sampleBody struct {
	ContentType *string       `json:"content_type,omitempty"`
	Content     *string       `json:"content,omitempty"`
	Parts       *[]sampleBody `json:"parts,omitempty"`
}
type sampleAttachment struct {
	Filename    *string `json:"filename,omitempty"`
	ContentType *string `json:"content_type,omitempty"`
	Size        *int64  `json:"size,omitempty"`
	Disposition *string `json:"disposition,omitempty"`
	ContentID   *string `json:"content_id,omitempty"`
	Content     *string `json:"content,omitempty"`
	Storage     *string `json:"storage,omitempty"`
	Ref         *string `json:"ref,omitempty"`
	Checksum    *string `json:"checksum,omitempty"`
}
type sampleCalendarEvent struct {
	Method      *string                  `json:"method,omitempty"`
	UID         *string                  `json:"uid,omitempty"`
	Summary     *string                  `json:"summary,omitempty"`
	Description *string                  `json:"description,omitempty"`
	Location    *string                  `json:"location,omitempty"`
	DTStart     *time.Time               `json:"dtstart,omitempty"`
	DTEnd       *time.Time               `json:"dtend,omitempty"`
	AllDay      *bool                    `json:"all_day,omitempty"`
	Organizer   *sampleCalendarAddress   `json:"organizer,omitempty"`
	Attendees   *[]sampleCalendarAddress `json:"attendees,omitempty"`
	Status      *string                  `json:"status,omitempty"`
	Sequence    *int                     `json:"sequence,omitempty"`
	DTStamp     *time.Time               `json:"dtstamp,omitempty"`
}
type sampleCalendarAddress struct {
	Address  *string `json:"address,omitempty"`
	Name     *string `json:"name,omitempty"`
	Role     *string `json:"role,omitempty"`
	PartStat *string `json:"partstat,omitempty"`
	RSVP     *bool   `json:"rsvp,omitempty"`
}
type sampleAddress struct {
	Name    *string `json:"name,omitempty"`
	Address *string `json:"address,omitempty"`
}

func (e *sampleEmail) canonical() *pipeline.EmailJSON {
	if e == nil {
		return nil
	}
	raw, _ := json.Marshal(e)
	var out pipeline.EmailJSON
	_ = json.Unmarshal(raw, &out)
	return &out
}
