package api

import (
	"strings"

	oapifly "github.com/antimatter-studios/go-oapifly"
)

// itemFormats are the formats of list items in a request body, by operation and list property.
// go-oapifly v0.35.0's field tags describe a list, and a format tag on one says nothing about
// its items. These go into the document only, and binding reads the types, so no answer changes
// whether or not a type is bound to the body: the drafts' handlers check each item themselves.
var itemFormats = map[string]map[string]string{
	"sendMessage":    {"to": "idn-email", "cc": "idn-email", "bcc": "idn-email"},
	"forwardMessage": {"to": "idn-email"},
	"saveDraft":      {"to": "idn-email", "cc": "idn-email"},
	"updateDraft":    {"to": "idn-email", "cc": "idn-email"},
}

// withItemFormats writes itemFormats into doc. The generator caches the map it generates and
// hands the same one to every caller, so this is done once, where the generator is made, before
// any caller has it. A registry without the operation has nothing to change;
// TestSendMessageDescribesWhatTheHandlerRequires and TestDraftBodiesDescribeWhatTheHandlersAccept
// hold the production description to this table.
func withItemFormats(doc map[string]interface{}) {
	paths, _ := doc["paths"].(map[string]map[string]oapifly.PathItem)
	components, _ := doc["components"].(map[string]interface{})
	schemas, _ := components["schemas"].(map[string]map[string]interface{})
	for _, methods := range paths {
		for _, operation := range methods {
			formats, ok := itemFormats[operation.OperationID]
			if !ok || operation.RequestBody == nil {
				continue
			}
			media, _ := operation.RequestBody.Content["application/json"].(map[string]interface{})
			schema, _ := media["schema"].(map[string]interface{})
			if ref, ok := schema["$ref"].(string); ok {
				schema = schemas[strings.TrimPrefix(ref, "#/components/schemas/")]
			}
			properties, _ := schema["properties"].(map[string]interface{})
			for property, format := range formats {
				list, _ := properties[property].(map[string]interface{})
				if items, ok := list["items"].(map[string]interface{}); ok {
					items["format"] = format
				}
			}
		}
	}
}

func withRequestConstraints(doc map[string]interface{}) {
	paths, _ := doc["paths"].(map[string]map[string]oapifly.PathItem)
	components, _ := doc["components"].(map[string]interface{})
	schemas, _ := components["schemas"].(map[string]map[string]interface{})
	for _, methods := range paths {
		for _, operation := range methods {
			if operation.RequestBody == nil {
				continue
			}
			media, _ := operation.RequestBody.Content["application/json"].(map[string]interface{})
			schema, _ := media["schema"].(map[string]interface{})
			if ref, ok := schema["$ref"].(string); ok {
				schema = schemas[strings.TrimPrefix(ref, "#/components/schemas/")]
			}
			properties, _ := schema["properties"].(map[string]interface{})
			switch operation.OperationID {
			case "bulkRetryQueue", "bulkBounceQueue", "bulkDeleteQueue":
				ids := properties["ids"].(map[string]interface{})
				items := ids["items"].(map[string]interface{})
				items["minimum"] = 0
				items["maximum"] = uint64(^uint(0))
			case "updateMessage":
				keywords := properties["keywords"].(map[string]interface{})
				items := keywords["items"].(map[string]interface{})
				items["minLength"] = 1
				items["maxLength"] = 255
				items["pattern"] = `^[\x21\x23\x24\x26\x27\x2b-\x5b\x5e-\x7a\x7c-\x7e]+$`
				keywords["description"] = "RFC 3501 ASCII keywords, without system flags; at most 100 distinct keywords, compared case-insensitively. Repeated keywords are accepted and deduplicated."
				folder := properties["folder"].(map[string]interface{})
				folder["description"] = "Valid UTF-8 folder name of at most 255 bytes; null leaves it unchanged. The byte limit also applies to multibyte names."
				for _, refinement := range folder["allOf"].([]interface{}) {
					refinement.(map[string]interface{})["type"] = []string{"string", "null"}
				}
			case "setDomainRelay":
				properties["username"].(map[string]interface{})["description"] = "At most 255 UTF-8 bytes, without ASCII control characters. For relay transport, a username and a password must be set together, or neither. An omitted or null password preserves the stored password."
				alternatives := schema["anyOf"].([]interface{})
				relay := alternatives[1].(map[string]interface{})["properties"].(map[string]interface{})
				relay["port"] = map[string]interface{}{"type": "integer"}
				relay["tls_mode"] = map[string]interface{}{"type": "string"}
			case "saveDraft", "updateDraft":
				from := properties["from"].(map[string]interface{})
				empty := []interface{}{""}
				if operation.OperationID == "updateDraft" {
					empty = append(empty, nil)
				}
				from["anyOf"] = []interface{}{
					map[string]interface{}{"type": from["type"], "enum": empty},
					map[string]interface{}{"type": "string", "format": "idn-email"},
				}
			case "setVacation":
				properties["enabled"].(map[string]interface{})["type"] = "boolean"
			}
		}
	}
}
