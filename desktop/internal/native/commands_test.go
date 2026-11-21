package native

import "testing"

func TestMenuCommandAvailability(t *testing.T) {
	for _, command := range Commands {
		t.Run(command.ID, func(t *testing.T) {
			if command.Accelerator == "" {
				t.Fatal("missing visible shortcut")
			}
			close := command.ID == "close-window"
			if (State{}).Allows(command, true) != close {
				t.Fatal("signed-out command enabled")
			}
			if (State{SignedIn: true, Editing: true, Viewing: true, MessageID: 1}).Allows(command, true) != close {
				t.Fatal("mail shortcut enabled while editing")
			}
			state := State{SignedIn: true, Viewing: true, MessageID: 1}
			if !state.Allows(command, true) {
				t.Fatal("applicable command disabled")
			}
			if state.Allows(command, false) != (close || command.ID == "new-message") {
				t.Fatal("message command leaked to compose window")
			}
			if command.Message {
				for _, state := range []State{{SignedIn: true}, {SignedIn: true, Viewing: true}, {SignedIn: true, Viewing: true, MessageID: 1, Draft: true}, {SignedIn: true, MessageID: 1}} {
					if state.Allows(command, true) {
						t.Fatal("inapplicable message command enabled")
					}
				}
			}
		})
	}
}

func TestMenuLabelsTrackReadAndFlaggedState(t *testing.T) {
	for _, command := range Commands {
		initial, changed := (State{}).Label(command), (State{Read: true, Flagged: true}).Label(command)
		switch command.ID {
		case "toggle-read":
			if initial != "Mark as Read" || changed != "Mark as Unread" {
				t.Fatalf("labels %q / %q", initial, changed)
			}
		case "toggle-flag":
			if initial != "Flag" || changed != "Unflag" {
				t.Fatalf("labels %q / %q", initial, changed)
			}
		default:
			if initial != command.Label || changed != command.Label {
				t.Fatal("unrelated label changed")
			}
		}
	}
}

func TestInboxBadgeLabel(t *testing.T) {
	for _, test := range []struct {
		unread int
		label  string
	}{{-1, ""}, {0, ""}, {1, "1"}, {129, "129"}, {9999, "9999"}, {10000, "9999+"}} {
		if label := BadgeLabel(test.unread); label != test.label {
			t.Errorf("badge %d = %q, want %q", test.unread, label, test.label)
		}
	}
}

func TestCloseMenuFollowsActiveTab(t *testing.T) {
	for _, command := range Commands {
		if command.ID == "close-window" && (State{ComposeTab: true}).Label(command) != "Close Tab" {
			t.Fatal("compose close label was not native")
		}
	}
}

func TestNotificationNeverRepeatsOrInterruptsTheViewedMessage(t *testing.T) {
	state := State{SignedIn: true, Viewing: true, MessageID: 42}
	if ShouldNotify(state, 42, true, false) {
		t.Fatal("notified for the message being read")
	}
	if ShouldNotify(state, 43, true, true) {
		t.Fatal("duplicate notification")
	}
	if ShouldNotify(State{}, 43, false, false) {
		t.Fatal("notification while signed out")
	}
	if ShouldNotify(state, 0, false, false) {
		t.Fatal("invalid message notification")
	}
	if !ShouldNotify(state, 43, true, false) {
		t.Fatal("new message suppressed")
	}
	if !ShouldNotify(state, 42, false, false) {
		t.Fatal("background arrival suppressed")
	}
	if !ShouldNotify(State{SignedIn: true, ComposeTab: true}, 43, true, false) {
		t.Fatal("new message suppressed while composing")
	}
}
