// Command ephdrop is a small command line driver for the ephdrop core.
// It exists to exercise the core during development.
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ephdrop:", err)
		os.Exit(1)
	}
}

func defaultDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "ephdrop")
}

func run(args []string) error {
	fs := flag.NewFlagSet("ephdrop", flag.ContinueOnError)
	dir := fs.String("dir", defaultDir(), "data directory")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ephdrop [-dir path] <command>")
		fmt.Fprintln(os.Stderr, "commands:")
		fmt.Fprintln(os.Stderr, "  id    show this device's id and public key (creates them on first run)")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("expected one command")
	}

	switch fs.Arg(0) {
	case "id":
		id, err := identity.LoadOrCreate(*dir)
		if err != nil {
			return err
		}
		fmt.Println("device id: ", id.ID())
		fmt.Println("public key:", base64.StdEncoding.EncodeToString(id.PublicKey()))
		fmt.Println("data dir:  ", *dir)
		return nil
	default:
		fs.Usage()
		return fmt.Errorf("unknown command %q", fs.Arg(0))
	}
}
