package native

import "strconv"

type Command struct {
	ID          string `json:"id"`
	Group       string `json:"group"`
	Label       string `json:"label"`
	Accelerator string `json:"accelerator"`
	Message     bool   `json:"-"`
}

var Commands = []Command{
	{ID: "new-message", Group: "File", Label: "New Message", Accelerator: "CmdOrCtrl+n"},
	{ID: "close-window", Group: "File", Label: "Close Window", Accelerator: "CmdOrCtrl+w"},
	{ID: "refresh", Group: "View", Label: "Refresh", Accelerator: "CmdOrCtrl+Shift+n"},
	{ID: "reply", Group: "Message", Label: "Reply", Accelerator: "CmdOrCtrl+r", Message: true},
	{ID: "reply-all", Group: "Message", Label: "Reply All", Accelerator: "CmdOrCtrl+Shift+r", Message: true},
	{ID: "forward", Group: "Message", Label: "Forward", Accelerator: "CmdOrCtrl+Shift+f", Message: true},
	{ID: "archive", Group: "Message", Label: "Archive", Accelerator: "Ctrl+Cmd+a", Message: true},
	{ID: "delete", Group: "Message", Label: "Delete", Accelerator: "CmdOrCtrl+backspace", Message: true},
	{ID: "toggle-read", Group: "Message", Label: "Mark as Unread", Accelerator: "CmdOrCtrl+Shift+u", Message: true},
	{ID: "toggle-flag", Group: "Message", Label: "Flag", Accelerator: "CmdOrCtrl+Shift+l", Message: true},
}

type State struct {
	SignedIn   bool   `json:"signedIn"`
	Editing    bool   `json:"editing"`
	Viewing    bool   `json:"viewing"`
	ComposeTab bool   `json:"composeTab"`
	ActiveTab  string `json:"activeTab"`
	MessageID  int    `json:"messageId"`
	Draft      bool   `json:"draft"`
	Read       bool   `json:"read"`
	Flagged    bool   `json:"flagged"`
}

func (state State) Allows(command Command, mainWindow bool) bool {
	if command.ID == "close-window" {
		return true
	}
	if !state.SignedIn || state.Editing {
		return false
	}
	if command.ID == "new-message" {
		return true
	}
	if !mainWindow {
		return false
	}
	return !command.Message || state.Viewing && state.MessageID > 0 && !state.Draft
}

func (state State) Label(command Command) string {
	if command.ID == "close-window" && state.ComposeTab {
		return "Close Tab"
	}
	if command.ID == "toggle-read" && !state.Read {
		return "Mark as Read"
	}
	if command.ID == "toggle-flag" && state.Flagged {
		return "Unflag"
	}
	return command.Label
}

func BadgeLabel(unread int) string {
	if unread <= 0 {
		return ""
	}
	if unread > 9999 {
		return "9999+"
	}
	return strconv.Itoa(unread)
}

func ShouldNotify(state State, identifier int, focused, duplicate bool) bool {
	return identifier > 0 && state.SignedIn && !duplicate && !(focused && state.Viewing && state.MessageID == identifier)
}
