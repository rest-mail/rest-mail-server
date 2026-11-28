// Package vcard encodes REST Mail contacts as vCard 4.0.
package vcard

import (
	"errors"
	"io"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

// Contact holds the stored fields that the contacts API returns and vCard preserves.
type Contact struct {
	ID         uint      `json:"id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	TrustLevel string    `json:"trust_level"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// WriteContact writes a single contact, with uid identifying it as a URI.
func WriteContact(writer io.Writer, uid string, contact Contact) error {
	address, err := mail.ParseAddress(contact.Email)
	if contact.ID == 0 || err != nil || address.Address != contact.Email {
		return errors.New("the server returned a contact without an id or valid email")
	}
	name := contact.Name
	nameProperty := "FN"
	if name == "" {
		name = contact.Email
	} else if name == contact.Email {
		nameProperty += ";X-RESTMAIL-EXPLICIT-NAME=TRUE"
	}
	lines := []string{"BEGIN:VCARD", "VERSION:4.0", "UID:" + uid,
		nameProperty + ":" + escapeText(name), "EMAIL:" + escapeText(contact.Email)}
	for _, field := range []struct{ name, value string }{
		{"X-RESTMAIL-TRUST-LEVEL", contact.TrustLevel}, {"X-RESTMAIL-SOURCE", contact.Source},
	} {
		if field.value != "" {
			lines = append(lines, field.name+":"+escapeText(field.value))
		}
	}
	if !contact.CreatedAt.IsZero() {
		lines = append(lines, "X-RESTMAIL-CREATED-AT:"+contact.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	if !contact.UpdatedAt.IsZero() {
		lines = append(lines, "REV:"+contact.UpdatedAt.UTC().Format("20060102T150405Z"), "X-RESTMAIL-UPDATED-AT:"+contact.UpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	for _, line := range append(lines, "END:VCARD") {
		if err := writeLine(writer, line); err != nil {
			return err
		}
	}
	return nil
}

func escapeText(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	return strings.NewReplacer("\\", "\\\\", ";", "\\;", ",", "\\,", "\n", "\\n").Replace(value)
}

func writeLine(writer io.Writer, line string) error {
	if !utf8.ValidString(line) || strings.ContainsFunc(line, func(character rune) bool {
		return character < 0x20 && character != '\t' || character == 0x7f
	}) {
		return errors.New("the server returned invalid vCard text")
	}
	width := 75
	for len(line) > width {
		end := width
		for !utf8.RuneStart(line[end]) {
			end--
		}
		if _, err := io.WriteString(writer, line[:end]+"\r\n "); err != nil {
			return err
		}
		line, width = line[end:], 74
	}
	_, err := io.WriteString(writer, line+"\r\n")
	return err
}
