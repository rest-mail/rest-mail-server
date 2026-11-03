package api

import (
	"strconv"

	oapifly "github.com/antimatter-studios/go-oapifly"
)

// responseLinks are the generated description's Link Objects, by operation and status: each
// create leads to the read, the changes and the delete of what it made, and a list to its
// first item, so a sequenced contract run fills their path parameters from a real answer
// rather than the examples' ids (#1232). They are the links openapi.yaml states.
// TestEveryCreateLeadsToItsLifecycle holds each create to that, or to a recorded reason.
var responseLinks = map[string]map[string]map[string]oapifly.Link{
	"addToAllowlist": {
		"201": {
			"removeFromAllowlist": {OperationID: "removeFromAllowlist", Parameters: map[string]string{"eid": "$response.body#/data/id", "id": "$request.path.id"}},
		},
	},
	"addToBlocklist": {
		"201": {
			"removeFromBlocklist": {OperationID: "removeFromBlocklist", Parameters: map[string]string{"eid": "$response.body#/data/id", "id": "$request.path.id"}},
		},
	},
	"blockSender": {
		"200": {
			"deleteContact": {OperationID: "deleteContact", Parameters: map[string]string{"cid": "$response.body#/data/id", "id": "$request.path.id"}},
			"updateContact": {OperationID: "updateContact", Parameters: map[string]string{"cid": "$response.body#/data/id", "id": "$request.path.id"}},
		},
		"201": {
			"deleteContact": {OperationID: "deleteContact", Parameters: map[string]string{"cid": "$response.body#/data/id", "id": "$request.path.id"}},
			"updateContact": {OperationID: "updateContact", Parameters: map[string]string{"cid": "$response.body#/data/id", "id": "$request.path.id"}},
		},
	},
	"bounceQueueEntry": {
		"200": {
			"deleteQueueEntry": {OperationID: "deleteQueueEntry", Parameters: map[string]string{"id": "$request.path.id"}},
		},
	},
	"createAPIKey": {
		"201": {
			"revokeAPIKey": {OperationID: "revokeAPIKey", Parameters: map[string]string{"credential_id": "$response.body#/data/id", "id": "$request.path.id"}},
			"updateAPIKey": {OperationID: "updateAPIKey", Parameters: map[string]string{"credential_id": "$response.body#/data/id", "id": "$request.path.id"}},
		},
	},
	"createAccount": {
		"201": {
			"deleteMailbox": {OperationID: "deleteMailbox", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"getMailbox":    {OperationID: "getMailbox", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"updateMailbox": {OperationID: "updateMailbox", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
	"createAlias": {
		"201": {
			"deleteAlias": {OperationID: "deleteAlias", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"getAlias":    {OperationID: "getAlias", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"updateAlias": {OperationID: "updateAlias", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
	"createAppPassword": {
		"201": {
			"revokeCredential": {OperationID: "revokeCredential", Parameters: map[string]string{"credential_id": "$response.body#/data/id", "id": "$request.path.id"}},
		},
	},
	"createBan": {
		"201": {
			"deleteBan": {OperationID: "deleteBan", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
	"createCertificate": {
		"201": {
			"getCertificate":    {OperationID: "getCertificate", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"deleteCertificate": {OperationID: "deleteCertificate", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
	"createClientSecret": {
		"201": {
			"revokeClientSecret": {OperationID: "revokeClientSecret", Parameters: map[string]string{"credential_id": "$response.body#/data/id", "id": "$request.path.id"}},
		},
	},
	"createContact": {
		"201": {
			"deleteContact": {OperationID: "deleteContact", Parameters: map[string]string{"cid": "$response.body#/data/id", "id": "$request.path.id"}},
			"updateContact": {OperationID: "updateContact", Parameters: map[string]string{"cid": "$response.body#/data/id", "id": "$request.path.id"}},
		},
	},
	"createCustomFilter": {
		"201": {
			"getCustomFilter":    {OperationID: "getCustomFilter", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"deleteCustomFilter": {OperationID: "deleteCustomFilter", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"testCustomFilter":   {OperationID: "testCustomFilter", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"updateCustomFilter": {OperationID: "updateCustomFilter", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
	"createDomain": {
		"201": {
			"addToAllowlist":     {OperationID: "addToAllowlist", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"addToBlocklist":     {OperationID: "addToBlocklist", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"deleteDomain":       {OperationID: "deleteDomain", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"domainDNSCheck":     {OperationID: "domainDNSCheck", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"getDKIMRotation":    {OperationID: "getDKIMRotation", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"getDomain":          {OperationID: "getDomain", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"getDomainRelay":     {OperationID: "getDomainRelay", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"listAllowlist":      {OperationID: "listAllowlist", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"listBlocklist":      {OperationID: "listBlocklist", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"listSuppressions":   {OperationID: "listSuppressions", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"setDKIMKey":         {OperationID: "setDKIMKey", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"setDKIMRotation":    {OperationID: "setDKIMRotation", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"setDomainRelay":     {OperationID: "setDomainRelay", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"setMtaStsPolicy":    {OperationID: "setMtaStsPolicy", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"stopDomainOutbound": {OperationID: "stopDomainOutbound", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"updateDomain":       {OperationID: "updateDomain", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
	"createFolder": {
		"201": {
			"deleteFolder": {OperationID: "deleteFolder", Parameters: map[string]string{"folder": "$response.body#/data/name", "id": "$request.path.id"}},
			"renameFolder": {OperationID: "renameFolder", Parameters: map[string]string{"folder": "$response.body#/data/name", "id": "$request.path.id"}},
		},
	},
	"createGrant": {
		"201": {
			"deleteGrant": {OperationID: "deleteGrant", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
	"createPipeline": {
		"201": {
			"deletePipeline": {OperationID: "deletePipeline", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"updatePipeline": {OperationID: "updatePipeline", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
	"deleteMtaStsPolicy": {
		"204": {
			"deleteDomain": {OperationID: "deleteDomain", Parameters: map[string]string{"id": "$request.path.id"}},
		},
	},
	"getCertificate": {
		"200": {
			"deleteCertificate": {OperationID: "deleteCertificate", Parameters: map[string]string{"id": "$request.path.id"}},
		},
	},
	"getMessage": {
		"200": {
			"getThread":             {OperationID: "getThread", Parameters: map[string]string{"id": "$response.body#/data/mailbox_id", "threadID": "$response.body#/data/thread_id"}},
			"getUnsubscribeOptions": {OperationID: "getUnsubscribeOptions", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"listMessageRecipients": {OperationID: "listMessageRecipients", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"verifyMessageSeal":     {OperationID: "verifyMessageSeal", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
	"getMtaStsPolicy": {
		"200": {
			"deleteMtaStsPolicy": {OperationID: "deleteMtaStsPolicy", Parameters: map[string]string{"id": "$request.path.id"}},
		},
	},
	"getRawMessage": {
		"200": {
			"deleteMessage": {OperationID: "deleteMessage", Parameters: map[string]string{"id": "$request.path.id"}},
		},
	},
	"getSieveScript": {
		"200": {
			"deleteSieveScript": {OperationID: "deleteSieveScript", Parameters: map[string]string{"id": "$request.path.id"}},
		},
	},
	"linkAccount": {
		"201": {
			"getAccount":    {OperationID: "getAccount", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"unlinkAccount": {OperationID: "unlinkAccount", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
	"listAccounts": {
		"200": {
			"blockSender":         {OperationID: "blockSender", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"createAppPassword":   {OperationID: "createAppPassword", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"createContact":       {OperationID: "createContact", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"createFolder":        {OperationID: "createFolder", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"disableVacation":     {OperationID: "disableVacation", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"getAccount":          {OperationID: "getAccount", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"getQuota":            {OperationID: "getQuota", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"getQuietHours":       {OperationID: "getQuietHours", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"getVacation":         {OperationID: "getVacation", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"importContacts":      {OperationID: "importContacts", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"listCalendarEvents":  {OperationID: "listCalendarEvents", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"listContacts":        {OperationID: "listContacts", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"listCredentials":     {OperationID: "listCredentials", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"listFolders":         {OperationID: "listFolders", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"listMessages":        {OperationID: "listMessages", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"listQuarantine":      {OperationID: "listQuarantine", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"putSieveScript":      {OperationID: "putSieveScript", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"searchMessages":      {OperationID: "searchMessages", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"setVacation":         {OperationID: "setVacation", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"setQuietHours":       {OperationID: "setQuietHours", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"suggestContacts":     {OperationID: "suggestContacts", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
			"validateSieveScript": {OperationID: "validateSieveScript", Parameters: map[string]string{"id": "$response.body#/data/0/id"}},
		},
	},
	"listQueue": {
		"200": {
			"bounceQueueEntry": {OperationID: "bounceQueueEntry", Parameters: map[string]string{"id": "$response.body#/data/items/0/id"}},
			"getQueueEntry":    {OperationID: "getQueueEntry", Parameters: map[string]string{"id": "$response.body#/data/items/0/id"}},
			"retryQueueEntry":  {OperationID: "retryQueueEntry", Parameters: map[string]string{"id": "$response.body#/data/items/0/id"}},
		},
	},
	"putSieveScript": {
		"200": {
			"deleteSieveScript": {OperationID: "deleteSieveScript", Parameters: map[string]string{"id": "$request.path.id"}},
			"getSieveScript":    {OperationID: "getSieveScript", Parameters: map[string]string{"id": "$request.path.id"}},
		},
		"201": {
			"deleteSieveScript": {OperationID: "deleteSieveScript", Parameters: map[string]string{"id": "$request.path.id"}},
			"getSieveScript":    {OperationID: "getSieveScript", Parameters: map[string]string{"id": "$request.path.id"}},
		},
	},
	"registerPushDevice": {
		"200": {
			"removePushDevice": {OperationID: "removePushDevice", Parameters: map[string]string{"device_id": "$response.body#/data/id"}},
		},
		"201": {
			"removePushDevice": {OperationID: "removePushDevice", Parameters: map[string]string{"device_id": "$response.body#/data/id"}},
		},
	},
	"registerRecoveryEmail": {
		"200": {
			"getRecoveryEmail": {OperationID: "getRecoveryEmail"},
		},
	},
	"renameFolder": {
		"200": {
			"deleteFolder": {OperationID: "deleteFolder", Parameters: map[string]string{"folder": "$response.body#/data/name", "id": "$request.path.id"}},
		},
	},
	"saveDraft": {
		"201": {
			"updateDraft": {OperationID: "updateDraft", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
	// A sent message that carries a calendar_event keeps the invitation in its Sent copy,
	// which respondToCalendar replies to. A scheduled send (202) is what
	// cancelScheduledMessage calls back (#1163).
	"sendMessage": {
		"201": {
			"deleteMessage":     {OperationID: "deleteMessage", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"forwardMessage":    {OperationID: "forwardMessage", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"getMessage":        {OperationID: "getMessage", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"getMessageTrace":   {OperationID: "getMessageTrace", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"getRawMessage":     {OperationID: "getRawMessage", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"listAttachments":   {OperationID: "listAttachments", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"respondToCalendar": {OperationID: "respondToCalendar", Parameters: map[string]string{"id": "$response.body#/data/id"}},
			"updateMessage":     {OperationID: "updateMessage", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
		"202": {
			"cancelScheduledMessage": {OperationID: "cancelScheduledMessage", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
	"setDKIMKey": {
		"200": {
			"deleteDKIMKey": {OperationID: "deleteDKIMKey", Parameters: map[string]string{"id": "$request.path.id"}},
		},
	},
	"setMtaStsPolicy": {
		"201": {
			"deleteMtaStsPolicy": {OperationID: "deleteMtaStsPolicy", Parameters: map[string]string{"id": "$request.path.id"}},
			"getMtaStsPolicy":    {OperationID: "getMtaStsPolicy", Parameters: map[string]string{"id": "$request.path.id"}},
			"mtaStsPolicyFile":   {OperationID: "mtaStsPolicyFile"},
		},
	},
	"stopDomainOutbound": {
		"200": {
			"restartDomainOutbound": {OperationID: "restartDomainOutbound", Parameters: map[string]string{"id": "$request.path.id"}},
		},
	},
	"testCustomFilter": {
		"200": {
			"deleteCustomFilter": {OperationID: "deleteCustomFilter", Parameters: map[string]string{"id": "$request.path.id"}},
		},
	},
	"updateDraft": {
		"200": {
			"sendDraft": {OperationID: "sendDraft", Parameters: map[string]string{"id": "$response.body#/data/id"}},
		},
	},
}

// withResponseLinks is endpoints with responseLinks on their responses. The registry's
// endpoints are not changed: each one given links gets its own copy of its responses.
func withResponseLinks(endpoints []oapifly.Endpoint) []oapifly.Endpoint {
	out := make([]oapifly.Endpoint, len(endpoints))
	for i, e := range endpoints {
		links, ok := responseLinks[e.OperationID]
		if ok {
			responses := append([]oapifly.EndpointResponse(nil), e.Responses...)
			for j, r := range responses {
				if l, ok := links[strconv.Itoa(r.Status)]; ok {
					responses[j].Links = l
				}
			}
			e.Responses = responses
		}
		out[i] = e
	}
	return out
}
