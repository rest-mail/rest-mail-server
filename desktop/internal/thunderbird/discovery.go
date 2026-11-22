package thunderbird

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type Folder struct {
	ID          string `json:"id"`
	Account     string `json:"account"`
	Name        string `json:"name"`
	Destination string `json:"destination"`
	Bytes       int64  `json:"bytes"`
	Messages    int64  `json:"messages"`
	Expunged    int64  `json:"expunged"`
	Problem     string `json:"problem,omitempty"`
}

type Profile struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Folders []Folder `json:"folders"`
	path    string
}

type Catalog struct {
	Profiles []Profile `json:"profiles"`
	Scanning bool      `json:"scanning"`
	Running  bool      `json:"running"`
	Problem  string    `json:"problem,omitempty"`
}

func profileRoots(home, platform, appData string) []string {
	switch platform {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Thunderbird")}
	case "windows":
		return []string{filepath.Join(appData, "Thunderbird")}
	default:
		return []string{filepath.Join(home, ".thunderbird"), filepath.Join(home, ".config", "thunderbird")}
	}
}

func Discover() ([]Profile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.New("could not locate Thunderbird profiles")
	}
	return discoverRoots(profileRoots(home, runtime.GOOS, os.Getenv("APPDATA")))
}

func discoverRoots(roots []string) ([]Profile, error) {
	profiles := []Profile{}
	seen := map[string]bool{}
	for _, root := range roots {
		file, err := os.Open(filepath.Join(root, "profiles.ini"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("could not read Thunderbird profiles.ini")
		}
		entries, err := readINI(io.LimitReader(file, 1<<20))
		file.Close()
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry["section"], "Profile") || entry["Path"] == "" {
				continue
			}
			path := filepath.FromSlash(entry["Path"])
			if entry["IsRelative"] != "0" {
				path = filepath.Join(root, path)
			}
			path, err = filepath.Abs(path)
			if err != nil || seen[path] {
				continue
			}
			info, err := os.Lstat(path)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			seen[path] = true
			digest := sha256.Sum256([]byte(path))
			profile := Profile{ID: hex.EncodeToString(digest[:16]), Name: entry["Name"], path: path, Folders: []Folder{}}
			if profile.Name == "" {
				profile.Name = filepath.Base(path)
			}
			if err := discoverFolders(&profile); err != nil {
				return nil, err
			}
			profiles = append(profiles, profile)
		}
	}
	return profiles, nil
}

func readINI(reader io.Reader) ([]map[string]string, error) {
	entries := []map[string]string{}
	var current map[string]string
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = map[string]string{"section": line[1 : len(line)-1]}
			entries = append(entries, current)
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok && current != nil {
			current[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if scanner.Err() != nil {
		return nil, errors.New("invalid Thunderbird profiles.ini")
	}
	return entries, nil
}

func discoverFolders(profile *Profile) error {
	accountIndex := 0
	for _, kind := range []string{"Mail", "ImapMail"} {
		accounts, err := os.ReadDir(filepath.Join(profile.path, kind))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return errors.New("could not list Thunderbird accounts")
		}
		for _, account := range accounts {
			if !account.IsDir() || account.Type()&os.ModeSymlink != 0 {
				continue
			}
			accountIndex++
			label := accountLabel(kind, account.Name(), accountIndex)
			rel := filepath.Join(kind, account.Name())
			if err := walkFolders(profile, rel, "", label); err != nil {
				return err
			}
		}
	}
	return nil
}

func accountLabel(kind, directory string, index int) string {
	if kind == "Mail" && strings.EqualFold(directory, "Local Folders") {
		return "Local Folders"
	}
	if position := strings.LastIndex(directory, "@"); position >= 0 {
		directory = directory[position+1:]
	}
	prefix := "Mail account"
	if kind == "ImapMail" {
		prefix = "IMAP cache"
	}
	return fmt.Sprintf("%s %d · %s", prefix, index, directory)
}

func walkFolders(profile *Profile, rel, parent, account string) error {
	entries, err := os.ReadDir(filepath.Join(profile.path, rel))
	if err != nil {
		return errors.New("could not list Thunderbird folders")
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if entry.IsDir() {
			if strings.HasSuffix(name, ".sbd") {
				if err := walkFolders(profile, filepath.Join(rel, name), joinFolder(parent, strings.TrimSuffix(name, ".sbd")), account); err != nil {
					return err
				}
			}
			continue
		}
		if !entry.Type().IsRegular() || strings.HasSuffix(strings.ToLower(name), ".msf") || name == "msgFilterRules.dat" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return errors.New("could not inspect Thunderbird folder")
		}
		if info.Size() > 0 {
			file, openErr := os.Open(filepath.Join(profile.path, rel, name))
			if openErr == nil {
				var prefix [5]byte
				count, readErr := io.ReadFull(file, prefix[:])
				file.Close()
				if readErr == nil && string(prefix[:count]) != "From " || readErr == io.ErrUnexpectedEOF {
					continue
				}
			}
		}
		fullName := joinFolder(parent, name)
		profile.Folders = append(profile.Folders, Folder{ID: filepath.ToSlash(filepath.Join(rel, name)), Account: account, Name: fullName, Destination: destination(fullName), Bytes: info.Size(), Messages: -1})
	}
	return nil
}

func joinFolder(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

func destination(name string) string {
	parts := strings.Split(name, "/")
	switch strings.ToLower(parts[0]) {
	case "inbox":
		parts[0] = "INBOX"
	case "sent", "sent items", "sent messages":
		parts[0] = "Sent"
	case "drafts":
		parts[0] = "Drafts"
	case "trash", "deleted", "deleted items":
		parts[0] = "Trash"
	case "archives", "archive":
		parts[0] = "Archive"
	}
	return strings.Join(parts, "/")
}

func countMessages(ctx context.Context, source io.Reader) (int64, int64, error) {
	reader := bufio.NewReaderSize(source, 64<<10)
	var messages, expunged int64
	inHeaders, continuation, started, deleted := false, false, false, false
	finish := func() {
		if started {
			if deleted {
				expunged++
			} else {
				messages++
			}
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		line, err := reader.ReadSlice('\n')
		if !continuation {
			if strings.HasPrefix(string(line), "From ") {
				finish()
				started, inHeaders, deleted = true, true, false
			} else if inHeaders {
				text := strings.TrimSpace(string(line))
				if text == "" {
					inHeaders = false
				}
				key, value, _ := strings.Cut(text, ":")
				if strings.EqualFold(key, "X-Mozilla-Status") {
					status, parseErr := strconv.ParseUint(strings.TrimSpace(value), 16, 16)
					if parseErr == nil && status&8 != 0 {
						deleted = true
					}
				}
			} else if !started && len(line) > 0 {
				return 0, 0, errors.New("not a From-framed mbox folder")
			}
		}
		continuation = errors.Is(err, bufio.ErrBufferFull)
		if errors.Is(err, io.EOF) {
			finish()
			return messages, expunged, nil
		}
		if err != nil && !continuation {
			return 0, 0, errors.New("could not count source messages")
		}
	}
}

func thunderbirdRunning() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2e9)
	defer cancel()
	if runtime.GOOS == "windows" {
		output, err := exec.CommandContext(ctx, "tasklist", "/FI", "IMAGENAME eq thunderbird.exe", "/NH").Output()
		return err == nil && strings.Contains(strings.ToLower(string(output)), "thunderbird.exe")
	}
	output, err := exec.CommandContext(ctx, "ps", "-A", "-o", "comm=").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(output), "\n") {
		name := strings.ToLower(filepath.Base(strings.TrimSpace(line)))
		if name == "thunderbird" || name == "thunderbird-bin" {
			return true
		}
	}
	return false
}
