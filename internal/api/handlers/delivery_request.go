package handlers

// deliveryImportOnlyFields are the fields a delivery once took that only an import may set
// (#1474). Older callers still send them; a delivery takes no notice of them, so arriving mail
// gets the server's time, the inbound rules' folder and default flags. The delivery route reads
// its body from disk and leaves these fields out as it reads (spooledBodySpec.ignored).
var deliveryImportOnlyFields = map[string]bool{
	"folder": true, "is_answered": true, "is_read": true, "is_flagged": true, "is_starred": true,
	"is_draft": true, "received_at": true, "mailbox_id": true,
}
