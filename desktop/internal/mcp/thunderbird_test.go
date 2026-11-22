package mcp

import (
	"encoding/json"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/thunderbird"
)

type fakeImports struct {
	selection thunderbird.Selection
	cancelled bool
}

func (fake *fakeImports) Profiles() thunderbird.Catalog {
	return thunderbird.Catalog{Profiles: []thunderbird.Profile{{ID: "sample", Name: "Synthetic", Folders: []thunderbird.Folder{}}}}
}
func (fake *fakeImports) Start(selection thunderbird.Selection) (thunderbird.Status, error) {
	fake.selection = selection
	return thunderbird.Status{State: "running", Selection: selection}, nil
}
func (fake *fakeImports) Status(cancel bool) thunderbird.Status {
	fake.cancelled = cancel
	return thunderbird.Status{State: "cancelled", Imported: 2, Duplicates: 1}
}

func TestThunderbirdMCPTools(t *testing.T) {
	domain := NewDomain(&fakeSession{}, &fakeMail{})
	imports := &fakeImports{}
	domain.Imports = imports
	_, discovery := startTestServer(t, Options{Domain: domain})
	client := connect(t, discovery)
	for _, item := range []struct {
		name string
		args any
	}{{"thunderbird_profiles", map[string]any{}}, {"thunderbird_import", thunderbird.Selection{ProfileID: "sample", AccountID: 7, Folders: []string{"Mail/Local Folders/Inbox"}}}, {"import_status", ImportStatusArgs{Cancel: true}}} {
		if result := call(t, client, item.name, item.args); result.IsError {
			t.Fatalf("%s failed", item.name)
		}
	}
	if imports.selection.AccountID != 7 || len(imports.selection.Folders) != 1 || !imports.cancelled {
		t.Fatal("MCP arguments not forwarded")
	}
	for _, name := range []string{"thunderbird_import", "import_status"} {
		if _, err := domain.Call(t.Context(), name, json.RawMessage(`{`)); err == nil {
			t.Fatal("malformed arguments accepted")
		}
	}
	domain.Imports = nil
	if _, err := domain.Call(t.Context(), "thunderbird_profiles", json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing service accepted")
	}
}
