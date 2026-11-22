package native

import (
	"fmt"
	"strings"
)

type MailNotice struct {
	ID      int    `json:"id"`
	Sender  string `json:"sender"`
	Subject string `json:"subject"`
}

type MailNotification struct {
	MessageID int
	Title     string
	Body      string
}

func NewMailNotification(messages []MailNotice) MailNotification {
	if len(messages) == 0 {
		return MailNotification{}
	}
	if len(messages) == 1 {
		message := messages[0]
		if message.Sender == "" {
			message.Sender = "Unknown sender"
		}
		if message.Subject == "" {
			message.Subject = "(No subject)"
		}
		return MailNotification{MessageID: message.ID, Title: message.Sender, Body: message.Subject}
	}
	senders := []string{}
	seen := map[string]bool{}
	for _, message := range messages {
		sender := message.Sender
		if sender == "" {
			sender = "Unknown sender"
		}
		if !seen[sender] && len(senders) < 3 {
			senders = append(senders, sender)
			seen[sender] = true
		}
	}
	return MailNotification{Title: fmt.Sprintf("%d new messages", len(messages)), Body: "From " + strings.Join(senders, ", ")}
}

func UnreadNotification(unread int) MailNotification {
	if unread <= 0 {
		return MailNotification{}
	}
	noun := "messages"
	if unread == 1 {
		noun = "message"
	}
	return MailNotification{Title: fmt.Sprintf("You have %d unread %s", unread, noun)}
}
