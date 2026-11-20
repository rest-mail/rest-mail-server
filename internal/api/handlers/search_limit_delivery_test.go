package handlers

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// The "unexplained" 500 for a message with many parameters at 10 MiB (#1915, #1921, #2107 AC04) was
// PostgreSQL refusing the full-text index's tsvector: "string is too long for tsvector (9481634 bytes,
// max 1048575 bytes)". It happens whenever a message's text holds more than about a megabyte of
// distinct words, which the same parameters do once a part is read as text, and which ordinary text can
// too. At 1 MiB the text can't pass the limit, so the same shapes were stored there. The message is
// legal, the gateway turns the 500 into a 451, and the sender retries until it gives up: mail lost.
//
// Each shape goes through /deliver into a schema with the production index, at both sizes. Each is
// stored whole, its text is whole, and search still finds it by a word near the start, through the
// index.
func TestLongTextOfManyWordsIsStoredAndSearchable(t *testing.T) {
	// importProofDB builds the production index, withMessageSearch.
	send, db, mailbox := deliverLimitSend(t)
	const id = "From: old@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n"
	for _, size := range []int{1 << 20, 10 << 20} {
		// many is first, then each with its number twice, until three quarters of size.
		many := func(first, each string) (string, string) {
			var b strings.Builder
			b.WriteString(first)
			last := ""
			for i := 0; b.Len() < size*3/4; i++ {
				last = fmt.Sprintf(each, i, i)
				b.WriteString(last)
			}
			return b.String(), strings.Trim(last, ";\r\n ")
		}
		ascii := strings.Repeat("hello world\r\n", size/52)
		parameters, lastParameter := many("Content-Type: text/plain; charset=utf-8", ";\r\n p%dq=v%dw")
		words, lastWord := many("", "w%dx%dy ")
		for name, shape := range map[string]struct{ message, last string }{
			// An unclosed quote in the boundary makes the whole multipart one text, part head and all.
			"parameters, in a multipart read as text": {id + "Content-Type: multipart/mixed; boundary=B; x=\"b\r\n\r\n--B\r\n" + parameters + "\r\n\r\n" + ascii + "--B--\r\n", lastParameter},
			"parameters, in a text part":              {id + "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\n" + parameters + "\r\n--B--\r\n", lastParameter},
			"distinct words":                          {id + "Content-Type: text/plain\r\n\r\n" + words + "\r\n", lastWord},
		} {
			t.Run(fmt.Sprintf("%s, %d MiB", name, size>>20), func(t *testing.T) {
				message := fmt.Sprintf("Message-ID: <search-%d-%s@example.test>\r\n", size, strings.ReplaceAll(name, " ", "-")) + shape.message
				w, _ := send(message)
				if w.Code != http.StatusCreated {
					t.Fatalf("/deliver = %d %s", w.Code, w.Body)
				}
				var stored models.Message
				if err := db.Where("mailbox_id = ?", mailbox.ID).Order("id DESC").First(&stored).Error; err != nil {
					t.Fatal(err)
				}
				if source := string(stored.Source()); !strings.HasSuffix(source, message) {
					t.Fatalf("the stored source isn't the message: %d bytes for %d", len(source), len(message))
				}
				if !strings.Contains(stored.BodyText, shape.last) {
					t.Fatalf("the stored text was cut: %d bytes, without %q", len(stored.BodyText), shape.last)
				}

				// What search runs, made to use the index, so the query and the index are the one expression.
				tx := db.Begin()
				defer tx.Rollback()
				if err := tx.Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
					t.Fatal(err)
				}
				var plan []string
				query := "SELECT id FROM messages WHERE mailbox_id = ? AND " + models.MessageSearchVectorSQL + " @@ plainto_tsquery('english', ?)"
				rows, err := tx.Raw("EXPLAIN SELECT id FROM messages WHERE "+models.MessageSearchVectorSQL+" @@ plainto_tsquery('english', ?)", "hello").Rows()
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var line string
					if err := rows.Scan(&line); err != nil {
						t.Fatal(err)
					}
					plan = append(plan, line)
				}
				if err := rows.Close(); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(strings.Join(plan, "\n"), "idx_messages_search_text") {
					t.Fatalf("search doesn't use the index:\n%s", strings.Join(plan, "\n"))
				}
				first := "charset"
				if name == "distinct words" {
					first = "w1x1y"
				}
				var found []uint
				if err := tx.Raw(query, mailbox.ID, first).Scan(&found).Error; err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(found, stored.ID) {
					t.Fatalf("search for %q found %v, not %d", first, found, stored.ID)
				}
			})
		}
	}
}
