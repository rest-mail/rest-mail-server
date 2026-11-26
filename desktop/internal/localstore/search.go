package localstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
	"golang.org/x/net/idna"
)

type Hit struct {
	apitypes.Message
	Snippet string  `json:"snippet"`
	Rank    float64 `json:"rank"`
	Source  string  `json:"source"`
}

type Results struct {
	Data       []Hit  `json:"data"`
	Source     string `json:"source"`
	Complete   bool   `json:"complete"`
	Offline    bool   `json:"offline"`
	Pagination struct {
		Cursor  string `json:"cursor,omitempty"`
		HasMore bool   `json:"has_more"`
	} `json:"pagination"`
}

func terms(query string) ([]string, error) {
	if len(query) > 4096 {
		return nil, errors.New("search query too long")
	}
	var result []string
	var term strings.Builder
	quoted := false
	for _, character := range query {
		if character == '"' {
			quoted = !quoted
			continue
		}
		if unicode.IsSpace(character) && !quoted {
			if term.Len() > 0 {
				result = append(result, term.String())
				term.Reset()
			}
		} else {
			term.WriteRune(character)
		}
	}
	if quoted {
		return nil, errors.New("unclosed search quote")
	}
	if term.Len() > 0 {
		result = append(result, term.String())
	}
	if len(result) > 64 {
		return nil, errors.New("too many search terms")
	}
	return result, nil
}

func searchSQL(query, folder string) (string, []any, string, error) {
	parts, err := terms(query)
	if err != nil {
		return "", nil, "", err
	}
	conditions := []string{"1=1"}
	arguments := []any{}
	fts := []string{}
	for _, part := range parts {
		operator, _, found := strings.Cut(part, ":")
		if found && strings.EqualFold(operator, "in") {
			folder = ""
		}
	}
	if folder != "" {
		conditions = append(conditions, "m.folder=?")
		arguments = append(arguments, folder)
	}
	for _, part := range parts {
		operator, value, found := strings.Cut(part, ":")
		operator = strings.ToLower(operator)
		if !found {
			fts = append(fts, quote(part))
			continue
		}
		if value == "" {
			return "", nil, "", errors.New("empty search operator")
		}
		switch strings.ToLower(operator) {
		case "from", "to", "subject":
			column := map[string]string{"from": "sender", "to": "recipients_to", "subject": "subject"}[strings.ToLower(operator)]
			if strings.Contains(value, "@") {
				local, domain, _ := strings.Cut(value, "@")
				if ascii, domainErr := idna.Lookup.ToASCII(domain); domainErr == nil {
					value = local + "@" + ascii
				}
			}
			fts = append(fts, column+":"+quote(value))
		case "has":
			if value != "attachment" {
				return "", nil, "", errors.New("unknown has operator")
			}
			conditions = append(conditions, "m.has_attachments=1")
		case "is":
			if value != "unread" {
				return "", nil, "", errors.New("unknown is operator")
			}
			conditions = append(conditions, "m.is_read=0")
		case "in":
			conditions = append(conditions, "m.folder=? COLLATE NOCASE")
			arguments = append(arguments, value)
		case "before", "after":
			date, dateErr := time.Parse("2006-01-02", value)
			if dateErr != nil {
				return "", nil, "", errors.New("dates must use YYYY-MM-DD")
			}
			comparison := ">="
			if operator == "before" {
				comparison = "<"
			}
			conditions = append(conditions, "m.received_at"+comparison+"?")
			arguments = append(arguments, date.Unix())
		default:
			return "", nil, "", fmt.Errorf("unknown search operator: %s", operator)
		}
	}
	return strings.Join(conditions, " AND "), arguments, strings.Join(fts, " AND "), nil
}

func (store *Store) Search(ctx context.Context, query, folder string, limit, offset int) (Results, error) {
	result := Results{Data: []Hit{}, Source: "local"}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 || offset > 1000000 {
		return result, errors.New("invalid search offset")
	}
	condition, arguments, fts, err := searchSQL(query, folder)
	if err != nil {
		return result, err
	}
	statement := `SELECT m.metadata,substr(m.search_body,1,180),0 FROM messages m WHERE ` + condition + ` ORDER BY m.received_at DESC,m.id DESC LIMIT ? OFFSET ?`
	if fts != "" {
		statement = `SELECT m.metadata,snippet(message_fts,-1,'⟦','⟧','…',24),bm25(message_fts,8,4,2,1,1)
 FROM message_fts JOIN messages m ON m.id=message_fts.rowid WHERE message_fts MATCH ? AND ` + condition + ` ORDER BY bm25(message_fts,8,4,2,1,1),m.received_at DESC,m.id DESC LIMIT ? OFFSET ?`
		arguments = append([]any{fts}, arguments...)
	}
	arguments = append(arguments, limit+1, offset)
	rows, err := store.reader.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var hit Hit
		var metadata string
		if err = rows.Scan(&metadata, &hit.Snippet, &hit.Rank); err != nil {
			rows.Close()
			return result, err
		}
		if err = json.Unmarshal([]byte(metadata), &hit.Message); err != nil {
			rows.Close()
			return result, err
		}
		hit.Source = "local"
		result.Data = append(result.Data, hit)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(result.Data) > limit {
		result.Data = result.Data[:limit]
		result.Pagination.HasMore = true
		result.Pagination.Cursor = fmt.Sprintf("local:%d", offset+limit)
	}
	status, err := store.Status(ctx)
	result.Complete = status.Complete
	return result, err
}
