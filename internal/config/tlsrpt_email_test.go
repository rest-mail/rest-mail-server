package config

import "testing"

func TestLoadTLSReportAddress(t *testing.T) {
	for _, address := range []string{"", "reports@example.test", " report@example.test ", "not-an-address", "Name <reports@example.test>", "a@example.test,b@example.test", "a@example.test\r\nBcc:x@example.test"} {
		t.Run(address, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("TLSRPT_REPORT_ADDRESS", address)
			cfg, err := Load()
			valid := address == "" || address == "reports@example.test" || address == " report@example.test "
			if valid && err != nil {
				t.Fatal(err)
			}
			if !valid && err == nil {
				t.Fatal("invalid reporting address accepted")
			}
			if address == " report@example.test " && cfg.TLSRPTReportAddress != "report@example.test" {
				t.Fatal("address not trimmed")
			}
		})
	}
}
