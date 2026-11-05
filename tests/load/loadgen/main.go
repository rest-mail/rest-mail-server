// Command loadgen drives load at a REST Mail topology for tests/load/run.py:
// SMTP messages (inbound, or submission with -user), authenticated IMAP
// connections held open, and an SMTP sink, the remote MX that counts the mail
// actually delivered. Each writes one JSON result. allow sets up the testbed for
// them: it puts the inbound sender on a domain's allowlist.
//
//	loadgen smtp -addr restmail.test:25 -rate 100 -duration 60s -out smtp_in.json
//	loadgen smtp -kind smtp_out -addr restmail.test:465 -tls -user u -pass p -to load@loadgen.test -run r1
//	loadgen imap -addr imap.restmail.test:993 -conns 1000 -duration 60s
//	loadgen sink -addr :25 -run r1 -sent-file smtp_out.json -drain 300s -duration 10m -out smtp_delivery.json
//	loadgen allow -api http://10.99.0.20:8080 -user admin@restmail.test -pass p -domain restmail.test -sender load@loadgen.test
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: loadgen smtp|imap|sink|allow [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "smtp":
		err = runSMTP(os.Args[2:])
	case "imap":
		err = runIMAP(os.Args[2:])
	case "sink":
		err = runSink(os.Args[2:])
	case "allow":
		err = runAllow(os.Args[2:])
	default:
		err = fmt.Errorf("unknown load %q (want smtp, imap, sink or allow)", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}
