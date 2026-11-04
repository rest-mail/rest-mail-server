package mailimport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	goimap "github.com/rest-mail/go-imap/pkg/imap"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// IMAPOptions identifies a source account. Credentials are used only on the
// verified TLS connection, never returned in errors or written to disk.
type IMAPOptions struct {
	Address, Username, Password string
	AccessToken                 string
	StartTLS                    bool // Require a verified upgrade before any credentials.
	TLSConfig                   *tls.Config
	// Renamed, if not nil, hears each source folder imported under another
	// name than its own, before any message is read.
	Renamed func(FolderRename)
}

// IMAP reads all selectable source folders over implicit TLS or an explicitly
// requested, mandatory STARTTLS upgrade. EXAMINE and BODY.PEEK keep flags and
// source messages unchanged. The visitor receives one
// bounded original message; rerun deduplication belongs to the destination API.
func IMAP(ctx context.Context, options IMAPOptions, maxBytes int64, visit Visit) error {
	if maxBytes < 1 || maxBytes > 1<<30 {
		return errors.New("message size limit must be between 1 byte and 1 GiB")
	}
	host, _, err := net.SplitHostPort(options.Address)
	if err != nil || host == "" || options.Username == "" || (options.Password == "" && options.AccessToken == "") {
		return errors.New("give an IMAP source address, username and password or access token")
	}
	if options.Password != "" && options.AccessToken != "" {
		return errors.New("give either an IMAP source password or an access token, not both")
	}
	tlsConfig := &tls.Config{}
	if options.TLSConfig != nil {
		tlsConfig = options.TLSConfig.Clone()
	}
	if tlsConfig.InsecureSkipVerify {
		return errors.New("IMAP source certificate verification cannot be disabled")
	}
	tlsConfig.ServerName = host
	if tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 30 * time.Second}, Config: tlsConfig}
	var conn net.Conn
	if options.StartTLS {
		conn, err = dialer.NetDialer.DialContext(ctx, "tcp", options.Address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", options.Address)
	}
	if err != nil {
		return errors.New("could not connect to the IMAP source with verified TLS")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var c *imapclient.Client
	if options.StartTLS {
		c, err = imapclient.NewStartTLS(conn, &imapclient.Options{TLSConfig: tlsConfig})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("could not secure the IMAP source with verified STARTTLS")
		}
	} else {
		c = imapclient.New(conn, nil)
	}
	defer func() { _ = c.Close() }()
	if err := c.WaitGreeting(); err != nil {
		return errors.New("IMAP source greeting failed")
	}
	if options.AccessToken != "" {
		var auth sasl.Client
		caps := c.Caps()
		switch {
		case caps.Has(imap.AuthCap(sasl.OAuthBearer)):
			auth = sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{Username: options.Username, Token: options.AccessToken})
		case caps.Has(imap.AuthCap("XOAUTH2")):
			auth = &imapXOAUTH2Client{username: options.Username, token: options.AccessToken}
		default:
			return errors.New("IMAP source does not advertise OAUTHBEARER or XOAUTH2 over verified TLS")
		}
		if err := c.Authenticate(auth); err != nil {
			return errors.New("IMAP source OAuth sign-in failed; obtain a valid access token and rerun")
		}
	} else {
		if err := c.Login(options.Username, options.Password).Wait(); err != nil {
			return errors.New("IMAP source sign-in failed")
		}
	}
	folders, err := c.List("", "*", nil).Collect()
	if err != nil {
		return errors.New("could not list IMAP source folders")
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Mailbox < folders[j].Mailbox })
	var claims []folderClaim
	for _, folder := range folders {
		if imapUnselectable(folder.Attrs) {
			continue
		}
		name := folder.Mailbox
		if folder.Delim != 0 && folder.Delim != '/' {
			name = strings.ReplaceAll(name, string(folder.Delim), "/")
		}
		name = foldInbox(name)
		reason := ""
		if problem := rmail.FolderNameProblem(name); problem != "" {
			name, reason = safeFolder(name), "not a valid folder name ("+problem+")"
		}
		claims = append(claims, folderClaim{source: folder.Mailbox, name: name, reason: reason})
	}
	// Name every folder before importing anything; names never stop the import.
	destinations := make(map[string]string, len(claims))
	for mailbox, name := range planFolders(claims, options.Renamed) {
		destinations[name] = mailbox
	}
	names := make([]string, 0, len(destinations))
	for name := range destinations {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		selected, err := c.Select(destinations[name], &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return errors.New("could not examine an IMAP source folder")
		}
		if selected.UIDValidity == 0 || selected.UIDNext == 0 {
			return errors.New("IMAP source omitted its UID identity")
		}
		if selected.NumMessages == 0 {
			continue
		}
		// Freeze the UID upper bound for this pass. Later arrivals are imported
		// by the next run; no source-side checkpoint or flag is written.
		if selected.UIDNext <= 1 {
			return errors.New("IMAP source returned inconsistent UID metadata")
		}
		set := imap.UIDSet{{Start: 1, Stop: selected.UIDNext - 1}}
		section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Size: maxBytes + 1}}
		fetch := c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true, InternalDate: true, RFC822Size: true, BodySection: []*imap.FetchItemBodySection{section}})
		abort := func(message *imapclient.FetchMessageData) {
			// Closing the socket first makes draining the client's literal
			// handoff bounded. Client.Close alone can wait for that handoff.
			_ = conn.Close()
			if message != nil {
				for message.Next() != nil {
				}
			}
			_ = fetch.Close()
		}
		seen := make(map[imap.UID]bool)
		for message := fetch.Next(); message != nil; message = fetch.Next() {
			body, uid, err := readIMAPMessage(message, section, name, maxBytes)
			if err != nil {
				// Close rather than drain an oversized or rejected literal.
				abort(message)
				return err
			}
			if seen[uid] || uid >= selected.UIDNext {
				abort(nil)
				return errors.New("IMAP source returned duplicate or inconsistent UIDs")
			}
			seen[uid] = true
			if err := ctx.Err(); err != nil {
				abort(nil)
				return err
			}
			if err := visit(fmt.Sprintf("IMAP UID %d", uid), body); err != nil {
				abort(nil)
				return err
			}
		}
		if err := fetch.Close(); err != nil {
			return errors.New("IMAP source message transfer failed")
		}
		if uint64(len(seen)) != uint64(selected.NumMessages) {
			return errors.New("IMAP source changed during import or omitted messages; rerun to resume")
		}
	}
	if err := c.Logout().Wait(); err != nil {
		return errors.New("IMAP source did not complete logout")
	}
	return nil
}

type imapXOAUTH2Client struct {
	username, token string
}

func (auth *imapXOAUTH2Client) Start() (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + auth.username + "\x01auth=Bearer " + auth.token + "\x01\x01"), nil
}

func (auth *imapXOAUTH2Client) Next([]byte) ([]byte, error) {
	return []byte{}, nil
}

func imapUnselectable(attrs []imap.MailboxAttr) bool {
	for _, attr := range attrs {
		if strings.EqualFold(string(attr), `\Noselect`) || strings.EqualFold(string(attr), `\NonExistent`) {
			return true
		}
	}
	return false
}

func readIMAPMessage(message *imapclient.FetchMessageData, section *imap.FetchItemBodySection, folder string, maxBytes int64) (apitypes.ImportMessageBody, imap.UID, error) {
	body := apitypes.ImportMessageBody{Folder: folder}
	var uid imap.UID
	var flags, date, size, raw bool
	var octets int64
	for item := message.Next(); item != nil; item = message.Next() {
		switch item := item.(type) {
		case imapclient.FetchItemDataUID:
			uid = item.UID
		case imapclient.FetchItemDataFlags:
			flags = true
			for _, flag := range item.Flags {
				switch strings.ToLower(string(flag)) {
				case `\seen`:
					body.Flags = append(body.Flags, `\Seen`)
				case `\answered`:
					body.Flags = append(body.Flags, `\Answered`)
				case `\flagged`:
					body.Flags = append(body.Flags, `\Flagged`)
				case `\draft`:
					body.Flags = append(body.Flags, `\Draft`)
				case `\recent`:
					// Recent describes this source session, not persistent mail.
				default:
					body.Flags = append(body.Flags, string(flag))
				}
			}
			if normalized, err := goimap.NormalizeImportFlags(body.Flags); err != nil {
				return body, uid, fmt.Errorf("IMAP source flags: %w", err)
			} else {
				body.Flags = normalized
			}
		case imapclient.FetchItemDataInternalDate:
			body.InternalDate, date = item.Time, true
		case imapclient.FetchItemDataRFC822Size:
			octets, size = item.Size, true
			if octets < 0 || octets > maxBytes {
				return body, uid, errors.New("IMAP source message exceeds the size limit")
			}
		case imapclient.FetchItemDataBodySection:
			if raw || item.Literal == nil || !item.MatchCommand(section) {
				return body, uid, errors.New("IMAP source returned an unexpected message section")
			}
			var err error
			body.RawMessage, err = io.ReadAll(io.LimitReader(item.Literal, maxBytes+1))
			if err != nil || int64(len(body.RawMessage)) > maxBytes {
				return body, uid, errors.New("IMAP source message is truncated or exceeds the size limit")
			}
			raw = true
		}
	}
	if uid == 0 || !flags || !date || !size || !raw || int64(len(body.RawMessage)) != octets {
		return body, uid, errors.New("IMAP source returned incomplete or inconsistent message metadata")
	}
	return body, uid, nil
}
