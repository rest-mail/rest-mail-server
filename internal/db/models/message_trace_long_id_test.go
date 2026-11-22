package models

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestMessageTraceLongMessageIDSchema(t *testing.T) {
	parsed, err := schema.Parse(&MessageTrace{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	field := parsed.LookUpField("rfc_message_id")
	if field == nil {
		t.Fatal("missing rfc_message_id column")
	}
	if field.DataType != "text" {
		t.Errorf("rfc_message_id data type = %q, want text", field.DataType)
	}
	for _, index := range parsed.ParseIndexes() {
		if index.Name != "idx_message_traces_rfc_message_id" {
			continue
		}
		if index.Type != "hash" || len(index.Fields) != 1 || index.Fields[0].Field != field {
			t.Errorf("Message-ID index = %+v, want single-column hash index without a variable-length btree key", index)
		}
		return
	}
	t.Error("missing Message-ID lookup index")
}
