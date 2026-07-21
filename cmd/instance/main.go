// Command instance renders (and later scaffolds) rest-mail instance manifests.
//
// `instance render` flattens instances/<domain>/manifest.yml into a config.env
// that the Taskfile loads via dotenv. The rendering logic lives in
// internal/instance so it is covered by the standard test lane; this file is a
// thin CLI around it.
//
// Usage:
//
//	instance render [-o out.env] [-check] <manifest.yml>
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/restmail/restmail/internal/instance"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "render":
		renderCmd(os.Args[2:])
	case "scaffold":
		scaffoldCmd(os.Args[2:])
	case "dns-env":
		dnsEnvCmd(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "instance: unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `instance — rest-mail instance manifest tool

usage:
  instance render [-o out.env] [-check] <manifest.yml>
      Flatten a manifest into a config.env (Taskfile var names).
      -o     output path (default: <manifest-dir>/config.env)
      -check don't write; exit non-zero if the target is missing or stale

  instance scaffold [-dir instances] <domain>
      Create instances/<domain>/{manifest.yml,config.env,secrets.env} with a
      freshly-allocated IP block and random secrets. Brings up nothing.

  instance dns-env [-o out.env] <manifest.yml>
      Render the dns.env consumed by reference-dnsmasq render-fragment.
`)
}

func dnsEnvCmd(args []string) {
	fs := flag.NewFlagSet("dns-env", flag.ExitOnError)
	out := fs.String("o", "", "output path (default: stdout)")
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "instance dns-env: exactly one manifest path is required")
		os.Exit(2)
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fatal("read manifest: %v", err)
	}
	m, err := instance.Parse(raw)
	if err != nil {
		fatal("%s: %v", fs.Arg(0), err)
	}
	data, err := instance.DNSEnv(m)
	if err != nil {
		fatal("dns-env %s: %v", fs.Arg(0), err)
	}
	if *out == "" {
		if _, err := os.Stdout.Write(data); err != nil {
			fatal("write stdout: %v", err)
		}
		return
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fatal("write %s: %v", *out, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
}

func scaffoldCmd(args []string) {
	fs := flag.NewFlagSet("scaffold", flag.ExitOnError)
	dir := fs.String("dir", "instances", "instances directory")
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "instance scaffold: exactly one <domain> is required")
		os.Exit(2)
	}
	domain := fs.Arg(0)

	target := filepath.Join(*dir, domain)
	if _, err := os.Stat(target); err == nil {
		fatal("scaffold: %s already exists — refusing to overwrite", target)
	}

	res, err := instance.Scaffold(domain, *dir)
	if err != nil {
		fatal("scaffold: %v", err)
	}

	if err := os.MkdirAll(target, 0o755); err != nil {
		fatal("scaffold: %v", err)
	}
	for name, data := range map[string][]byte{
		"manifest.yml": res.Manifest,
		"config.env":   res.Config,
		"secrets.env":  res.Secrets,
	} {
		if err := os.WriteFile(filepath.Join(target, name), data, 0o644); err != nil {
			fatal("scaffold: write %s: %v", name, err)
		}
	}

	fmt.Fprintf(os.Stderr, "scaffolded %s (project rest-mail-%s)\n", domain, res.Slug)
	for _, name := range []string{"postgres", "api", "smtp-gateway", "imap-gateway", "pop3-gateway", "js-filter", "webmail", "admin"} {
		fmt.Fprintf(os.Stderr, "  %-13s %s\n", name, res.IPs[name])
	}
	fmt.Fprintf(os.Stderr, "  files: %s/{manifest.yml,config.env,secrets.env}\n", target)
}

func renderCmd(args []string) {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	out := fs.String("o", "", "output path (default: <manifest-dir>/config.env)")
	check := fs.Bool("check", false, "don't write; fail if target is missing or stale")
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "instance render: exactly one manifest path is required")
		os.Exit(2)
	}
	manifestPath := fs.Arg(0)

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		fatal("read manifest: %v", err)
	}
	m, err := instance.Parse(raw)
	if err != nil {
		fatal("%s: %v", manifestPath, err)
	}
	rendered, err := instance.Render(m)
	if err != nil {
		fatal("render %s: %v", manifestPath, err)
	}

	target := *out
	if target == "" {
		target = filepath.Join(filepath.Dir(manifestPath), "config.env")
	}

	if *check {
		existing, err := os.ReadFile(target)
		if err != nil {
			fatal("check: %s missing or unreadable (run `instance render %s`): %v", target, manifestPath, err)
		}
		if !bytes.Equal(existing, rendered) {
			fatal("check: %s is stale — re-run `instance render %s`", target, manifestPath)
		}
		return
	}

	if err := os.WriteFile(target, rendered, 0o644); err != nil {
		fatal("write %s: %v", target, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", target)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "instance: "+format+"\n", args...)
	os.Exit(1)
}
