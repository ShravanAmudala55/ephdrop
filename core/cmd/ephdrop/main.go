// Command ephdrop is a small command line driver for the ephdrop core.
// It exists to exercise the core during development.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
	"github.com/ShravanAmudala55/ephdrop/core/pairing"
)

const inviteLifetime = 5 * time.Minute

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

func defaultName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "this device"
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: ephdrop [-dir path] [-name device-name] <command>

commands:
  id             show this device's id and public key (creates them on first run)
  invite         print a pairing invite and wait for another device to join
  join <invite>  pair with the device that made the invite
  peers          list paired devices
  unpair <id>    forget a paired device`)
}

func run(args []string) error {
	fs := flag.NewFlagSet("ephdrop", flag.ContinueOnError)
	dir := fs.String("dir", defaultDir(), "data directory")
	name := fs.String("name", defaultName(), "name shown to other devices")
	fs.Usage = usage
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		usage()
		return errors.New("missing command")
	}

	cmd, rest := fs.Arg(0), fs.Args()[1:]
	switch cmd {
	case "id":
		return needArgs(cmd, rest, 0, func() error { return cmdID(*dir) })
	case "invite":
		return needArgs(cmd, rest, 0, func() error { return cmdInvite(*dir, *name) })
	case "join":
		return needArgs(cmd, rest, 1, func() error { return cmdJoin(*dir, *name, rest[0]) })
	case "peers":
		return needArgs(cmd, rest, 0, func() error { return cmdPeers(*dir) })
	case "unpair":
		return needArgs(cmd, rest, 1, func() error { return cmdUnpair(*dir, rest[0]) })
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func needArgs(cmd string, rest []string, n int, f func() error) error {
	if len(rest) != n {
		usage()
		return fmt.Errorf("%s: expected %d argument(s), got %d", cmd, n, len(rest))
	}
	return f()
}

func openStore(dir string) (*pairing.Store, error) {
	return pairing.OpenStore(filepath.Join(dir, "peers.json"))
}

func cmdID(dir string) error {
	id, err := identity.LoadOrCreate(dir)
	if err != nil {
		return err
	}
	fmt.Println("device id: ", id.ID())
	fmt.Println("public key:", base64.StdEncoding.EncodeToString(id.PublicKey()))
	fmt.Println("data dir:  ", dir)
	return nil
}

func cmdInvite(dir, name string) error {
	self, err := identity.LoadOrCreate(dir)
	if err != nil {
		return err
	}
	store, err := openStore(dir)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	addrs := pairing.LocalAddrs(port)
	if len(addrs) == 0 {
		ln.Close()
		return errors.New("no network address found. Are you connected to Wi-Fi?")
	}
	inv, err := pairing.NewInvite(self, addrs, inviteLifetime)
	if err != nil {
		ln.Close()
		return err
	}

	fmt.Printf("Invite (valid for %v):\n", inviteLifetime)
	fmt.Println()
	fmt.Println(inv.String())
	fmt.Println()
	fmt.Println("On the other device run:  ephdrop join <invite>")
	fmt.Println("Listening on", strings.Join(addrs, ", "), "... press Ctrl+C to cancel.")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	in := bufio.NewReader(os.Stdin)
	v := &pairing.Inviter{
		Self: self, Name: name, Store: store, Invite: inv,
		Confirm: func(p pairing.Peer) bool {
			fmt.Printf("\n%q (%s) wants to pair. Allow? [y/N] ", p.Name, p.ID.Short())
			line, _ := in.ReadString('\n')
			ans := strings.ToLower(strings.TrimSpace(line))
			return ans == "y" || ans == "yes"
		},
	}
	peer, err := v.Serve(ctx, ln)
	if err != nil {
		return err
	}
	fmt.Printf("Paired with %q (%s).\n", peer.Name, peer.ID.Short())
	return nil
}

func cmdJoin(dir, name, invite string) error {
	inv, err := pairing.ParseInvite(invite)
	if err != nil {
		return err
	}
	self, err := identity.LoadOrCreate(dir)
	if err != nil {
		return err
	}
	store, err := openStore(dir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Println("Connecting... the other device may ask its user to confirm.")
	peer, err := pairing.Join(ctx, self, name, store, inv)
	if err != nil {
		return err
	}
	fmt.Printf("Paired with %q (%s).\n", peer.Name, peer.ID.Short())
	return nil
}

func cmdPeers(dir string) error {
	store, err := openStore(dir)
	if err != nil {
		return err
	}
	peers := store.List()
	if len(peers) == 0 {
		fmt.Println("No paired devices.")
		return nil
	}
	for _, p := range peers {
		fmt.Printf("%-24s %s  paired %s\n", p.Name, p.ID, p.Added.Local().Format("2006-01-02 15:04"))
	}
	return nil
}

func cmdUnpair(dir, id string) error {
	store, err := openStore(dir)
	if err != nil {
		return err
	}
	// Allow a unique prefix, since full ids are long.
	var match []pairing.Peer
	for _, p := range store.List() {
		if strings.HasPrefix(string(p.ID), strings.ToLower(id)) {
			match = append(match, p)
		}
	}
	switch len(match) {
	case 0:
		return fmt.Errorf("no paired device with id starting %q", id)
	case 1:
		if err := store.Remove(match[0].ID); err != nil {
			return err
		}
		fmt.Printf("Unpaired %q (%s).\n", match[0].Name, match[0].ID.Short())
		return nil
	default:
		return fmt.Errorf("%q matches %d devices, give more of the id", id, len(match))
	}
}
