// Package wire is the IMAP4rev1 lexical layer the server parses commands and
// writes responses with: command lines and tags, atoms, quoted strings and
// literals (RFC 3501 §4.3), astrings and nstrings, sequence and UID sets, flag
// lists, date-times and the FETCH data-item grammar (RFC 3501 §9). It knows
// nothing about sessions or mail stores.
package wire
