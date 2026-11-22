package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/rest-mail/rest-mail-server/desktop/internal/mailbackend"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	"github.com/rest-mail/rest-mail-server/desktop/internal/thunderbird"
	shared "github.com/rest-mail/rest-mail-server/pkg/mailimport"
)

type Imports struct{ service *thunderbird.Service }

func newImports(manager *mailbackend.Manager) *Imports {
	config, _ := os.UserConfigDir()
	name := "import-progress.json"
	if instance := currentInstance(); instance != "" {
		name = "import-progress-" + instance + ".json"
	}
	service := thunderbird.New(nil, filepath.Join(config, "REST Mail", name))
	service.Prepare = func(account uint32) (shared.Request, error) {
		status := manager.Manager.Status()
		if !status.SignedIn || account == 0 {
			return nil, errors.New("sign in to a REST Mail server before importing")
		}
		return func(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
			return manager.Manager.Do(session.BindServer(ctx, status.Server), method, path, body)
		}, nil
	}
	return &Imports{service: service}
}

func (imports *Imports) Profiles() thunderbird.Catalog { return imports.service.Profiles() }
func (imports *Imports) RefreshProfiles() thunderbird.Catalog {
	return imports.service.RefreshProfiles()
}
func (imports *Imports) Start(selection thunderbird.Selection) (thunderbird.Status, error) {
	return imports.service.Start(selection)
}
func (imports *Imports) Status(cancel bool) thunderbird.Status { return imports.service.Status(cancel) }
