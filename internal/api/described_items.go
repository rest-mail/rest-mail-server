package api

import (
	"strings"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
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

// folderNamePatterns are rmail.FolderNameProblem as patterns, the ones updateMessage's folder
// tags state: something besides spaces, no space at either end, no control character, no "..".
// The first is the property's own pattern, the rest its allOf refinements.
var folderNamePatterns = []string{
	"[^\\x09-\\x0d \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]",
	"^[^\\x09-\\x0d \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]([\\s\\S]*[^\\x09-\\x0d \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000])?$",
	"^[^\\x00-\\x1f\\x7f-\\x9f]*$",
	"^([^.]|\\.[^.])*(\\.)?$",
}

// folderNameSchema is a folder name as rmail.FolderNameProblem takes it. It goes into the
// document only: the handlers that take one bind a plain string and make the refusal
// themselves, so neither the refusal nor its place in the order of refusals changes.
func folderNameSchema(description string) map[string]interface{} {
	refinements := make([]interface{}, 0, len(folderNamePatterns)-1)
	for _, pattern := range folderNamePatterns[1:] {
		refinements = append(refinements, map[string]interface{}{"type": "string", "pattern": pattern})
	}
	schema := map[string]interface{}{
		"type": "string", "minLength": 1, "maxLength": 255,
		"pattern": folderNamePatterns[0], "allOf": refinements,
	}
	if description != "" {
		schema["description"] = description
	}
	return schema
}

// folderNameDescription is what a folder name's schema says of it in words.
const folderNameDescription = "A folder name: valid UTF-8 of at most 255 bytes, neither blank nor beginning or ending " +
	`with a space, with no control character and no ".."; any other name is refused 422 validation_failed.`

// withPathConstraints states what the handlers refuse in a path parameter. subscribeFolder holds
// its name to the folder rule; unsubscribeFolder takes any name, so the parameter they share
// is replaced on subscribeFolder alone rather than changed where both read it.
func withPathConstraints(doc map[string]interface{}) {
	paths, _ := doc["paths"].(map[string]map[string]oapifly.PathItem)
	for _, methods := range paths {
		for method, operation := range methods {
			if operation.OperationID != "subscribeFolder" {
				continue
			}
			// A copy, in case the generator built the operations' lists from one array.
			parameters := append([]oapifly.Parameter(nil), operation.Parameters...)
			for index := range parameters {
				if parameters[index].In == "path" && parameters[index].Name == "folder" {
					parameters[index].Schema = folderNameSchema("")
					parameters[index].Description = folderNameDescription
				}
			}
			operation.Parameters = parameters
			methods[method] = operation
		}
	}
}

func withRequestConstraints(doc map[string]interface{}) {
	withPathConstraints(doc)
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
			case "createCustomFilter", "updateCustomFilter":
				// The field stays raw JSON, so the handler keeps its own refusal and its
				// place in the order of refusals; this says what that refusal requires.
				config := properties["config"].(map[string]interface{})
				config["type"] = "object"
				config["additionalProperties"] = map[string]interface{}{}
			case "probeServices":
				services := properties["services"].(map[string]interface{})
				services["items"].(map[string]interface{})["pattern"] = handlers.ProbeServicePattern
			case "setDomainRelay":
				properties["username"].(map[string]interface{})["description"] = "At most 255 UTF-8 bytes, without ASCII control characters. For relay transport, a username and a password must be set together, or neither, counting the stored password: an omitted or null password preserves it. A username with no password sent or stored is refused 422, and so is no username while a password is stored and not cleared with an empty one, even though each body is schema-valid."
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
			case "createFolder", "renameFolder":
				// Both bodies are folderNameBody, so this states the rule once for the two.
				properties["name"] = folderNameSchema(folderNameDescription)
			case "setVacation":
				properties["enabled"].(map[string]interface{})["type"] = "boolean"
			}
		}
	}
}
