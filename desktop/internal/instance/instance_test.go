package instance

import "testing"

func TestTestCopiesNeverRestoreOrReplaceTheOwnerKeychain(t *testing.T) {
	for _, test := range []struct {
		name    string
		service string
	}{{"", "REST Mail"}, {"devstack", "REST Mail test copy devstack"}, {" polish ", "REST Mail test copy polish"}, {"../owner", "REST Mail"}, {"UPPER", "REST Mail"}, {"with space", "REST Mail"}, {"a-very-long-name-that-goes-past-thirty-two-chars", "REST Mail"}} {
		t.Setenv("RESTMAIL_DESKTOP_INSTANCE", test.name)
		if got := KeychainService(); got != test.service {
			t.Errorf("instance %q keychain = %q, want %q", test.name, got, test.service)
		}
	}
}
