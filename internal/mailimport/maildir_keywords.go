package mailimport

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
)

func maildirKeywords(root *os.Root, folder string) (map[rune]string, error) {
	name := filepath.Join(folder, "dovecot-keywords")
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, errors.New("invalid Maildir keyword mapping file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 65537))
	keywords := make(map[rune]string)
	names := make(map[string]bool)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return nil, errors.New("invalid Maildir keyword mapping")
		}
		index, err := strconv.Atoi(fields[0])
		if err != nil || index < 0 || index > 25 || keywords[rune('a'+index)] != "" || names[strings.ToLower(fields[1])] {
			return nil, errors.New("invalid or duplicate Maildir keyword mapping index or name")
		}
		if _, err := goimap.NormalizeImportFlags(fields[1:]); err != nil || strings.Contains(fields[1], `\`) {
			return nil, errors.New("invalid Maildir keyword mapping: expected an RFC 3501 keyword")
		}
		keywords[rune('a'+index)] = fields[1]
		names[strings.ToLower(fields[1])] = true
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return keywords, nil
}
