package mailimport

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// FolderRename is a source folder an import stores under another name than the
// one it maps to, so that every message arrives. An import never stops or skips
// mail over a folder name.
type FolderRename struct {
	Source string `json:"source"`
	Folder string `json:"folder"`
	Reason string `json:"reason"`
}

// folderClaim is a source folder and the destination name it maps to. reason
// is non-empty when that name is already derived rather than the source's own.
type folderClaim struct {
	source, name, reason string
}

// planFolders gives every source folder a distinct destination name. A source
// whose own name maps cleanly wins over one whose name was derived, then source
// order decides. Every other source wanting the same name gets "<name> (2)",
// "(3)" … , the first such name no source claims. The plan depends only on the
// source names, so a rerun maps every folder the same way and resumes.
func planFolders(claims []folderClaim, renamed func(FolderRename)) map[string]string {
	ordered := slices.Clone(claims)
	slices.SortStableFunc(ordered, func(a, b folderClaim) int {
		if (a.reason == "") != (b.reason == "") {
			if a.reason == "" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.source, b.source)
	})
	plan := make(map[string]string, len(ordered))
	taken := make(map[string]bool, len(ordered))
	var losers []folderClaim
	for _, claim := range ordered {
		if taken[claim.name] {
			losers = append(losers, claim)
			continue
		}
		taken[claim.name] = true
		plan[claim.source] = claim.name
		if claim.reason != "" && renamed != nil {
			renamed(FolderRename{Source: claim.source, Folder: claim.name, Reason: claim.reason})
		}
	}
	for _, claim := range losers {
		name := claim.name
		for n := 2; taken[name]; n++ {
			name = numberedFolder(claim.name, n)
		}
		taken[name] = true
		plan[claim.source] = name
		reason := fmt.Sprintf("another source folder is already imported as %q", claim.name)
		if claim.reason != "" {
			reason = claim.reason + "; " + reason
		}
		if renamed != nil {
			renamed(FolderRename{Source: claim.source, Folder: name, Reason: reason})
		}
	}
	return plan
}

// numberedFolder is "<name> (n)", the name shortened so the result stays a
// legal folder name.
func numberedFolder(name string, n int) string {
	suffix := fmt.Sprintf(" (%d)", n)
	for len(name)+len(suffix) > rmail.MaxFolderNameLen {
		_, size := utf8.DecodeLastRuneInString(name)
		name = strings.TrimRight(name[:len(name)-size], " ")
	}
	return name + suffix
}

// safeFolder is name changed as little as possible into a legal REST Mail
// folder name: invalid UTF-8 and control characters become U+FFFD, ".." is
// split by a space, surrounding spaces go, an empty name is "Unnamed" and an
// overlong one is cut at a character boundary.
func safeFolder(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return utf8.RuneError
		}
		return r
	}, strings.ToValidUTF8(name, string(utf8.RuneError)))
	for strings.Contains(name, "..") {
		name = strings.ReplaceAll(name, "..", ". .")
	}
	for len(name) > rmail.MaxFolderNameLen {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	if name = strings.TrimSpace(name); name == "" {
		name = "Unnamed"
	}
	return rmail.CanonicalFolder(name)
}
