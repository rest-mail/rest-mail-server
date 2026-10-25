package handlers

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestFolderMutationPersistenceFailures(t *testing.T) {
	for _, operation := range []string{"count", "create", "rename"} {
		t.Run(operation, func(t *testing.T) {
			fx := newFolderRulesFixture(t)
			callback := "folder_mutation_" + operation + "_failure"
			fail := func(db *gorm.DB) {
				if db.Statement.Table == "messages" {
					db.AddError(errors.New("private folder database detail"))
				}
			}
			remove := func() {}
			switch operation {
			case "count":
				if err := fx.db.Callback().Query().Before("gorm:query").Register(callback, fail); err != nil {
					t.Fatal(err)
				}
				remove = func() { fx.db.Callback().Query().Remove(callback) }
			case "create":
				if err := fx.db.Callback().Create().Before("gorm:create").Register(callback, fail); err != nil {
					t.Fatal(err)
				}
				remove = func() { fx.db.Callback().Create().Remove(callback) }
			case "rename":
				if err := fx.db.Callback().Update().Before("gorm:update").Register(callback, fail); err != nil {
					t.Fatal(err)
				}
				remove = func() { fx.db.Callback().Update().Remove(callback) }
			}
			t.Cleanup(remove)
			method, folder := http.MethodPost, ""
			if operation == "rename" {
				method, folder = http.MethodPatch, "Work"
			}
			rr := fx.send(t, method, folder, `{"name":"New"}`)
			if rr.Code != 500 || strings.Contains(rr.Body.String(), "private") {
				t.Fatalf("%s failure: %d %s", operation, rr.Code, rr.Body)
			}
			remove()
			if fx.count(t, "New") != 0 || fx.count(t, "Work") != 2 {
				t.Fatal("failed mutation changed folder storage")
			}
		})
	}
}

func TestFolderMutationMarkerAndRenamePersistence(t *testing.T) {
	fx := newFolderRulesFixture(t)
	rr := fx.send(t, http.MethodPost, "", `{"name":"Empty"}`)
	if rr.Code != 201 {
		t.Fatalf("create:%d %s", rr.Code, rr.Body)
	}
	var marker models.Message
	if err := fx.db.Where("mailbox_id = ? AND folder = ?", fx.mailbox, "Empty").First(&marker).Error; err != nil {
		t.Fatal(err)
	}
	if !marker.IsDeleted || marker.SizeBytes != 0 || marker.Sender != "system" {
		t.Fatalf("not a zero-byte deleted marker:%+v", marker)
	}
	rr = fx.send(t, http.MethodPatch, "Empty", `{"name":"Empty/2026"}`)
	if rr.Code != 200 {
		t.Fatalf("rename marker:%d %s", rr.Code, rr.Body)
	}
	if fx.count(t, "Empty") != 0 || fx.count(t, "Empty/2026") != 1 || fx.count(t, "Work") != 2 {
		t.Fatal("marker rename changed unrelated folder")
	}
	var after models.Message
	if err := fx.db.First(&after, marker.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Folder != "Empty/2026" || !after.IsDeleted || after.SizeBytes != 0 {
		t.Fatalf("renamed marker changed:%+v", after)
	}
}
