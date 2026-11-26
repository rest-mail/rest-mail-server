package localstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
	_ "modernc.org/sqlite"
)

type Store struct {
	writer *sql.DB
	reader *sql.DB
	path   string
}

type Status struct {
	Messages int64 `json:"messages"`
	Bodies   int64 `json:"bodies"`
	Bytes    int64 `json:"bytes"`
	Complete bool  `json:"complete"`
}

func Root(config, instance string) (string, error) {
	if instance != "" {
		return os.MkdirTemp("", "restmail-sqlite-")
	}
	root := filepath.Join(config, "REST Mail", "mail")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	for _, directory := range []string{filepath.Dir(root), root} {
		info, err := os.Lstat(directory)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("local store refuses symlinks")
		}
	}
	if err := os.Chmod(filepath.Dir(root), 0700); err != nil {
		return "", err
	}
	return root, os.Chmod(root, 0700)
}

func AccountKey(server, owner string, account uint) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", server, owner, account)))
	return hex.EncodeToString(digest[:])
}

func Open(root, key string) (*Store, error) {
	if len(key) != 64 {
		return nil, errors.New("invalid local account key")
	}
	if _, err := hex.DecodeString(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, key+".sqlite")
	for _, candidate := range []string{root, path, path + "-wal", path + "-shm"} {
		info, err := os.Lstat(candidate)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("local store refuses symlinks")
		}
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String() + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	writer, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	store := &Store{writer: writer, path: path}
	if _, err = writer.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;"); err == nil {
		err = store.migrate()
	}
	if err != nil {
		writer.Close()
		return nil, err
	}
	reader, err := sql.Open("sqlite", uri+"&_pragma=query_only(1)")
	if err != nil {
		writer.Close()
		return nil, err
	}
	reader.SetMaxOpenConns(4)
	store.reader = reader
	return store, nil
}

var migrations = []string{`
CREATE TABLE accounts (id INTEGER PRIMARY KEY CHECK(id=1), identity TEXT NOT NULL);
CREATE TABLE folders (
 name TEXT PRIMARY KEY, metadata TEXT NOT NULL, uid_validity INTEGER NOT NULL DEFAULT 0,
 cursor TEXT NOT NULL DEFAULT '', generation INTEGER NOT NULL DEFAULT 0,
 scanning INTEGER NOT NULL DEFAULT 0, complete INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE messages (
 id INTEGER PRIMARY KEY, folder TEXT NOT NULL, thread_id TEXT NOT NULL,
 subject TEXT NOT NULL, sender TEXT NOT NULL, recipients_to TEXT NOT NULL, recipients_cc TEXT NOT NULL,
 received_at INTEGER NOT NULL, size_bytes INTEGER NOT NULL, is_read INTEGER NOT NULL,
 has_attachments INTEGER NOT NULL, metadata TEXT NOT NULL, search_body TEXT NOT NULL DEFAULT '',
 body_cached INTEGER NOT NULL DEFAULT 0, generation INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX messages_folder_date ON messages(folder, received_at DESC, id DESC);
CREATE INDEX messages_pending ON messages(body_cached, size_bytes);
CREATE TABLE bodies (message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
 text TEXT NOT NULL, html TEXT NOT NULL, raw_size INTEGER NOT NULL);
CREATE TABLE attachments (id INTEGER PRIMARY KEY, message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 filename TEXT NOT NULL, content_type TEXT NOT NULL, size_bytes INTEGER NOT NULL);
`, `
CREATE VIRTUAL TABLE message_fts USING fts5(subject, sender, recipients_to, recipients_cc, search_body,
 content='messages', content_rowid='id', tokenize='unicode61 remove_diacritics 2');
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
 INSERT INTO message_fts(rowid,subject,sender,recipients_to,recipients_cc,search_body)
 VALUES(new.id,new.subject,new.sender,new.recipients_to,new.recipients_cc,new.search_body); END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
 INSERT INTO message_fts(message_fts,rowid,subject,sender,recipients_to,recipients_cc,search_body)
 VALUES('delete',old.id,old.subject,old.sender,old.recipients_to,old.recipients_cc,old.search_body); END;
CREATE TRIGGER messages_au AFTER UPDATE ON messages WHEN
 old.subject!=new.subject OR old.sender!=new.sender OR old.recipients_to!=new.recipients_to OR
 old.recipients_cc!=new.recipients_cc OR old.search_body!=new.search_body BEGIN
 INSERT INTO message_fts(message_fts,rowid,subject,sender,recipients_to,recipients_cc,search_body)
 VALUES('delete',old.id,old.subject,old.sender,old.recipients_to,old.recipients_cc,old.search_body);
 INSERT INTO message_fts(rowid,subject,sender,recipients_to,recipients_cc,search_body)
 VALUES(new.id,new.subject,new.sender,new.recipients_to,new.recipients_cc,new.search_body); END;
INSERT INTO message_fts(message_fts) VALUES('rebuild');
`, `CREATE TABLE account_catalog(id INTEGER PRIMARY KEY, metadata TEXT NOT NULL);`, `
ALTER TABLE messages ADD COLUMN needs_body INTEGER NOT NULL DEFAULT 0;
CREATE INDEX messages_reindex ON messages(needs_body,body_cached,size_bytes);
`}

func (store *Store) migrate() error {
	if _, err := store.writer.Exec(`CREATE TABLE IF NOT EXISTS migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var version int
	if err := store.writer.QueryRow("SELECT coalesce(max(version),0) FROM migrations").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return errors.New("local store was created by a newer desktop version")
	}
	for index := version; index < len(migrations); index++ {
		transaction, err := store.writer.Begin()
		if err != nil {
			return err
		}
		if _, err = transaction.Exec(migrations[index]); err == nil {
			_, err = transaction.Exec("INSERT INTO migrations VALUES(?,?)", index+1, time.Now().UTC().Format(time.RFC3339))
		}
		if err != nil {
			transaction.Rollback()
			return fmt.Errorf("migration %d: %w", index+1, err)
		}
		if err = transaction.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) Close() error {
	readerError := store.reader.Close()
	_, checkpointError := store.writer.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return errors.Join(readerError, checkpointError, store.writer.Close())
}

func (store *Store) Delete() error {
	if err := store.Close(); err != nil {
		return err
	}
	for _, path := range []string{store.path, store.path + "-wal", store.path + "-shm"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (store *Store) Identity(ctx context.Context, key string) error {
	_, err := store.writer.ExecContext(ctx, "INSERT INTO accounts VALUES(1,?) ON CONFLICT(id) DO NOTHING", key)
	return err
}

func (store *Store) Catalog(ctx context.Context) ([]apitypes.AccountResponse, error) {
	rows, err := store.reader.QueryContext(ctx, "SELECT metadata FROM account_catalog ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []apitypes.AccountResponse{}
	for rows.Next() {
		var metadata string
		var account apitypes.AccountResponse
		if err = rows.Scan(&metadata); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(metadata), &account); err != nil {
			return nil, err
		}
		result = append(result, account)
	}
	return result, rows.Err()
}

func (store *Store) PutCatalog(ctx context.Context, accounts []apitypes.AccountResponse) error {
	transaction, err := store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err = transaction.ExecContext(ctx, "DELETE FROM account_catalog"); err != nil {
		return err
	}
	for _, account := range accounts {
		metadata, encodeError := json.Marshal(account)
		if encodeError != nil {
			return encodeError
		}
		if _, err = transaction.ExecContext(ctx, "INSERT INTO account_catalog VALUES(?,?)", account.ID, string(metadata)); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (store *Store) RemoveCatalog(ctx context.Context, id uint) error {
	_, err := store.writer.ExecContext(ctx, "DELETE FROM account_catalog WHERE id=?", id)
	return err
}

func (store *Store) FolderComplete(ctx context.Context, folder string) bool {
	var complete bool
	err := store.reader.QueryRowContext(ctx, "SELECT complete=1 AND scanning=0 FROM folders WHERE name=?", folder).Scan(&complete)
	return err == nil && complete
}

func (store *Store) Status(ctx context.Context) (Status, error) {
	var status Status
	err := store.reader.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(body_cached),0),
	 (SELECT count(*)>0 AND min(complete)=1 FROM folders) AND NOT EXISTS(SELECT 1 FROM messages WHERE needs_body=1) FROM messages`).Scan(&status.Messages, &status.Bodies, &status.Complete)
	status.Complete = status.Complete && status.Messages == status.Bodies
	for _, path := range []string{store.path, store.path + "-wal", store.path + "-shm"} {
		if info, statErr := os.Stat(path); statErr == nil {
			status.Bytes += info.Size()
		}
	}
	return status, err
}

func (store *Store) Put(ctx context.Context, message apitypes.Message, body bool) error {
	return store.PutBatch(ctx, []apitypes.Message{message}, body, 0)
}

func (store *Store) PutBatch(ctx context.Context, messages []apitypes.Message, body bool, generation int64) error {
	transaction, err := store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	for _, message := range messages {
		if message.ID == 0 || message.Folder == "" {
			return errors.New("message identity missing")
		}
		text, html := message.BodyText, message.BodyHTML
		message.BodyText, message.BodyHTML, message.Mailbox = "", "", nil
		metadata, err := json.Marshal(message)
		if err != nil {
			return err
		}
		_, err = transaction.ExecContext(ctx, `INSERT INTO messages
 (id,folder,thread_id,subject,sender,recipients_to,recipients_cc,received_at,size_bytes,is_read,has_attachments,metadata,search_body,body_cached,generation)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET
 folder=excluded.folder,thread_id=excluded.thread_id,subject=excluded.subject,sender=excluded.sender,
 recipients_to=excluded.recipients_to,recipients_cc=excluded.recipients_cc,received_at=excluded.received_at,
 size_bytes=excluded.size_bytes,is_read=excluded.is_read,has_attachments=excluded.has_attachments,metadata=excluded.metadata,
 search_body=CASE WHEN excluded.body_cached THEN excluded.search_body ELSE messages.search_body END,
 body_cached=max(messages.body_cached,excluded.body_cached),
 needs_body=CASE WHEN excluded.body_cached THEN 0 ELSE messages.needs_body END,
 generation=CASE WHEN excluded.generation>0 THEN excluded.generation ELSE messages.generation END`,
			message.ID, message.Folder, message.ThreadID, message.Subject, addresses(message.SenderName+" "+message.Sender), addresses(string(message.RecipientsTo)), addresses(string(message.RecipientsCc)), message.ReceivedAt.Unix(), message.SizeBytes, message.IsRead, message.HasAttachments, string(metadata), text+" "+HTMLText(html), body, generation)
		if err != nil {
			return err
		}
		if body {
			_, err = transaction.ExecContext(ctx, `INSERT INTO bodies VALUES(?,?,?,?) ON CONFLICT(message_id) DO UPDATE SET text=excluded.text,html=excluded.html,raw_size=excluded.raw_size`, message.ID, text, html, message.RawSize)
			if err != nil {
				return err
			}
		}
	}
	return transaction.Commit()
}

func (store *Store) Get(ctx context.Context, id uint, requireBody bool) (apitypes.Message, error) {
	var message apitypes.Message
	var metadata string
	var text, html sql.NullString
	err := store.reader.QueryRowContext(ctx, `SELECT metadata,b.text,b.html FROM messages m LEFT JOIN bodies b ON b.message_id=m.id
 WHERE m.id=? AND (?=0 OR m.body_cached=1)`, id, requireBody).Scan(&metadata, &text, &html)
	if err != nil {
		return message, err
	}
	if err = json.Unmarshal([]byte(metadata), &message); err != nil {
		return message, err
	}
	message.BodyText, message.BodyHTML = text.String, html.String
	return message, nil
}

func (store *Store) Remove(ctx context.Context, id uint) error {
	_, err := store.writer.ExecContext(ctx, "DELETE FROM messages WHERE id=?", id)
	return err
}

func (store *Store) Pending(ctx context.Context, budget int64) ([]apitypes.Message, error) {
	rows, err := store.reader.QueryContext(ctx, `SELECT metadata FROM messages WHERE (body_cached=0 OR needs_body=1) AND size_bytes<=? ORDER BY received_at DESC LIMIT 100`, budget)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []apitypes.Message{}
	for rows.Next() {
		var metadata string
		var message apitypes.Message
		if err = rows.Scan(&metadata); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(metadata), &message); err != nil {
			return nil, err
		}
		result = append(result, message)
	}
	return result, rows.Err()
}

func (store *Store) Reset(ctx context.Context) error {
	_, err := store.writer.ExecContext(ctx, `UPDATE folders SET cursor='',scanning=0,complete=0; UPDATE messages SET needs_body=1;`)
	return err
}

func (store *Store) Path() string { return store.path }

func (store *Store) PutAttachments(ctx context.Context, id uint, data []Attachment) error {
	transaction, err := store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err = transaction.ExecContext(ctx, "DELETE FROM attachments WHERE message_id=?", id); err != nil {
		return err
	}
	for _, attachment := range data {
		if _, err = transaction.ExecContext(ctx, `INSERT INTO attachments VALUES(?,?,?,?,?)`, attachment.ID, id, attachment.Filename, attachment.ContentType, attachment.SizeBytes); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

type Attachment struct {
	ID          uint   `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

func (store *Store) Attachments(ctx context.Context, id uint) ([]Attachment, error) {
	rows, err := store.reader.QueryContext(ctx, "SELECT id,filename,content_type,size_bytes FROM attachments WHERE message_id=?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Attachment{}
	for rows.Next() {
		var attachment Attachment
		if err = rows.Scan(&attachment.ID, &attachment.Filename, &attachment.ContentType, &attachment.SizeBytes); err != nil {
			return nil, err
		}
		result = append(result, attachment)
	}
	return result, rows.Err()
}

func (store *Store) Folders(ctx context.Context) ([]apitypes.FolderSummary, error) {
	rows, err := store.reader.QueryContext(ctx, "SELECT metadata FROM folders ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []apitypes.FolderSummary{}
	for rows.Next() {
		var metadata string
		var folder apitypes.FolderSummary
		if err = rows.Scan(&metadata); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(metadata), &folder); err != nil {
			return nil, err
		}
		result = append(result, folder)
	}
	return result, rows.Err()
}

func (store *Store) PutFolders(ctx context.Context, folders []apitypes.FolderSummary) error {
	transaction, err := store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	for _, folder := range folders {
		metadata, err := json.Marshal(folder)
		if err != nil {
			return err
		}
		if _, err = transaction.ExecContext(ctx, `INSERT INTO folders(name,metadata) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET metadata=excluded.metadata`, folder.Folder, string(metadata)); err != nil {
			return err
		}
	}
	rows, err := transaction.QueryContext(ctx, "SELECT name FROM folders")
	if err != nil {
		return err
	}
	var removed []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		found := false
		for _, folder := range folders {
			if folder.Folder == name {
				found = true
				break
			}
		}
		if !found {
			removed = append(removed, name)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range removed {
		if _, err = transaction.ExecContext(ctx, "DELETE FROM messages WHERE folder=?", name); err != nil {
			return err
		}
		if _, err = transaction.ExecContext(ctx, "DELETE FROM folders WHERE name=?", name); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (store *Store) BeginScan(ctx context.Context, folder string) (string, int64, error) {
	_, err := store.writer.ExecContext(ctx, `UPDATE folders SET generation=generation+CASE WHEN scanning=0 THEN 1 ELSE 0 END, scanning=1 WHERE name=?`, folder)
	if err != nil {
		return "", 0, err
	}
	var cursor string
	var generation int64
	err = store.reader.QueryRowContext(ctx, "SELECT cursor,generation FROM folders WHERE name=?", folder).Scan(&cursor, &generation)
	return cursor, generation, err
}

func (store *Store) SavePage(ctx context.Context, folder, cursor string, generation int64, validity uint32, messages []apitypes.Message, done bool) error {
	if err := store.PutBatch(ctx, messages, false, generation); err != nil {
		return err
	}
	transaction, err := store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if done {
		if _, err = transaction.ExecContext(ctx, "DELETE FROM messages WHERE folder=? AND generation<>?", folder, generation); err != nil {
			return err
		}
	}
	_, err = transaction.ExecContext(ctx, `UPDATE folders SET cursor=?,uid_validity=?,scanning=?,complete=? WHERE name=?`, cursor, validity, !done, done, folder)
	if err != nil {
		return err
	}
	return transaction.Commit()
}

func (store *Store) RestartScan(ctx context.Context, folder string) error {
	_, err := store.writer.ExecContext(ctx, "UPDATE folders SET cursor='',scanning=0,complete=0 WHERE name=?", folder)
	return err
}

func (store *Store) List(ctx context.Context, folder string, before int64, limit int) ([]apitypes.Message, bool, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := store.reader.QueryContext(ctx, `SELECT metadata FROM messages WHERE folder=? AND (?=0 OR id<?)
 AND coalesce(json_extract(metadata,'$.imap_deleted'),0)=0
 AND (json_extract(metadata,'$.snoozed_until') IS NULL OR unixepoch(json_extract(metadata,'$.snoozed_until'))<=unixepoch('now'))
 ORDER BY id DESC LIMIT ?`, folder, before, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := []apitypes.Message{}
	for rows.Next() {
		var metadata string
		var message apitypes.Message
		if err = rows.Scan(&metadata); err != nil {
			return nil, false, err
		}
		if err = json.Unmarshal([]byte(metadata), &message); err != nil {
			return nil, false, err
		}
		result = append(result, message)
	}
	more := len(result) > limit
	if more {
		result = result[:limit]
	}
	return result, more, rows.Err()
}

func quote(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
