package netguard

import (
	"net"
	"testing"
)

func TestNonPublic(t *testing.T) {
	cases := []struct {
		ip        string
		nonPublic bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"169.254.169.254", true},
		{"fe80::1", true},
		{"10.0.0.5", true},
		{"172.20.0.3", true},
		{"192.168.1.10", true},
		{"fd00::1", true},
		{"0.0.0.0", true},
		{"::", true},
		{"0.1.2.3", true},
		{"100.64.0.1", true},
		{"100.100.100.200", true},
		{"192.0.0.8", true},
		{"198.18.0.1", true},
		{"240.0.0.1", true},
		{"255.255.255.255", true},
		{"224.0.0.1", true},
		{"239.1.2.3", true},
		{"ff02::1", true},
		{"64:ff9b::a9fe:a9fe", true},
		{"64:ff9b:1::1", true},
		{"2002:a00:1::1", true},
		{"fec0::1", true},
		{"100::1", true},
		{"2001:db8::1", true},
		{"192.0.2.1", true},
		{"198.51.100.1", true},
		{"203.0.113.1", true},
		{"::ffff:10.0.0.1", true},
		{"::ffff:127.0.0.1", true},
		{"8.8.8.8", false},
		{"93.184.216.34", false},
		{"100.63.255.255", false},
		{"100.128.0.0", false},
		{"::ffff:8.8.8.8", false},
		{"2606:4700:4700::1111", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test ip %q", c.ip)
		}
		if got := NonPublic(ip); got != c.nonPublic {
			t.Errorf("NonPublic(%s) = %v, want %v", c.ip, got, c.nonPublic)
		}
	}
}
