package api

import (
	"embed"
	"encoding/json"
	"io/fs"
	"sort"
	"strings"

	oapifly "github.com/antimatter-studios/go-oapifly"
)

// strictAreas is advanced by the warning sync's ready PRs, one clean area at a
// time. Keeping operation IDs makes the scope independent of display tags.
//
//go:embed strict_areas*
var strictAreas embed.FS

func strictAreaScope() oapifly.StrictScope {
	var ids []string
	err := fs.WalkDir(strictAreas, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		data, err := strictAreas.ReadFile(path)
		if err != nil {
			return err
		}
		var areas map[string][]string
		if err := json.Unmarshal(data, &areas); err != nil {
			return err
		}
		for _, operations := range areas {
			ids = append(ids, operations...)
		}
		return nil
	})
	if err != nil {
		panic("invalid API strict-area configuration: " + err.Error())
	}
	sort.Strings(ids)
	return oapifly.StrictScope{Operations: ids}
}
