package api

import oapifly "github.com/antimatter-studios/go-oapifly"

// itemFormats are the formats of list items in a request body, by operation and list property.
// go-oapifly v0.35.0's field tags describe a list, and a format tag on one says nothing about
// its items. Each body named here is description only: nothing is bound to it, so no answer
// changes.
var itemFormats = map[string]map[string]string{
	"sendMessage": {"to": "idn-email", "cc": "idn-email", "bcc": "idn-email"},
}

// withItemFormats writes itemFormats into doc. The generator caches the map it generates and
// hands the same one to every caller, so this is done once, where the generator is made, before
// any caller has it. A registry without the operation has nothing to change;
// TestSendMessageDescribesWhatTheHandlerRequires holds the production description to this table.
func withItemFormats(doc map[string]interface{}) {
	paths, _ := doc["paths"].(map[string]map[string]oapifly.PathItem)
	for _, methods := range paths {
		for _, operation := range methods {
			formats, ok := itemFormats[operation.OperationID]
			if !ok || operation.RequestBody == nil {
				continue
			}
			media, _ := operation.RequestBody.Content["application/json"].(map[string]interface{})
			schema, _ := media["schema"].(map[string]interface{})
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
