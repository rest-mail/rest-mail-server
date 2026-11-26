package native

import "testing"

func TestLayoutMenuCheckmarks(t *testing.T) {
	for _, test := range []struct {
		identifier string
		state      State
		checked    bool
	}{
		{"view.layout.columns", State{}, true},
		{"view.layout.columns", State{Layout: "stacked"}, false},
		{"view.layout.stacked", State{Layout: "stacked"}, true},
		{"view.layout.stacked", State{Layout: "columns"}, false},
		{"view.pane.folders", State{Folders: true}, true},
		{"view.pane.folders", State{}, false},
		{"view.pane.reader", State{Reader: true}, true},
		{"view.pane.reader", State{}, false},
		{"refresh", State{}, false},
	} {
		t.Run(test.identifier, func(t *testing.T) {
			if checked := test.state.Checked(test.identifier); checked != test.checked {
				t.Fatalf("checked = %v, want %v", checked, test.checked)
			}
		})
	}
}
