package handlers

import (
	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/db/models"
)

const delegatedSendingDescription = "Sending from your own mailbox requires mail:send. From another mailbox requires mail:send_as on that mailbox. Set on_behalf to true to require mail:send_on_behalf instead: From stays the represented mailbox and RFC 5322 Sender is the authenticated account's own address. Read access grants neither mode. Both modes audit the real account, credential and represented mailbox."

// These alternatives describe the capability selected by the handler, not a
// requirement to hold all three. Source-message access remains independent.
func delegatedSendSecurity(source ...string) []oapifly.SecurityRequirement {
	var out []oapifly.SecurityRequirement
	for _, scheme := range folderSchemes {
		for _, capability := range []string{models.CapMailSend, models.CapMailSendAs, models.CapMailSendOnBehalf} {
			required := append(append([]string(nil), source...), capability)
			out = append(out, oapifly.SecurityRequirement{scheme: required})
		}
	}
	return out
}
