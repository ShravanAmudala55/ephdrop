package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/discovery"
	"github.com/ShravanAmudala55/ephdrop/core/discovery/mdns"
	"github.com/ShravanAmudala55/ephdrop/core/identity"
	"github.com/ShravanAmudala55/ephdrop/core/pairing"
	"github.com/ShravanAmudala55/ephdrop/core/shelf"
	"github.com/ShravanAmudala55/ephdrop/core/transfer"
)

const (
	sweepEvery  = time.Minute
	findTimeout = 10 * time.Second
)

// matchPeer finds the one paired device whose id starts with prefix.
func matchPeer(store *pairing.Store, prefix string) (pairing.Peer, error) {
	prefix = strings.ToLower(prefix)
	var match []pairing.Peer
	for _, p := range store.List() {
		if strings.HasPrefix(string(p.ID), prefix) {
			match = append(match, p)
		}
	}
	switch len(match) {
	case 0:
		return pairing.Peer{}, fmt.Errorf("no paired device with id starting %q (see: ephdrop peers)", prefix)
	case 1:
		return match[0], nil
	default:
		return pairing.Peer{}, fmt.Errorf("%q matches %d devices, give more of the id", prefix, len(match))
	}
}

func cmdServe(dir string, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", 0, "TCP port to listen on (0 picks one)")
	ttl := fs.Duration("ttl", 0, "how long the files given here stay shared (default 24h, at most 168h)")
	if err := fs.Parse(args); err != nil {
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
	if len(store.List()) == 0 {
		return errors.New("no paired devices yet. Pair one first with: ephdrop invite")
	}
	cert, err := self.TLSCertificate()
	if err != nil {
		return err
	}
	sh, err := shelf.Open(filepath.Join(dir, "shelf"), self.ID())
	if err != nil {
		return err
	}
	for _, path := range fs.Args() {
		if err := addFile(sh, path, *ttl); err != nil {
			return err
		}
	}
	if _, err := sh.Sweep(); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		return err
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Println("Sharing:")
	files := sh.List()
	if len(files) == 0 {
		fmt.Println("  (nothing yet)")
	}
	for _, e := range files {
		fmt.Printf("  %s  %-30s %10s  expires %s\n", e.ID, e.Name, humanSize(e.Size), e.Expires.Local().Format("Mon 15:04"))
	}
	fmt.Printf("Listening on port %d. Press Ctrl+C to stop.\n", p)

	go func() {
		t := time.NewTicker(sweepEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := sh.Sweep(); err != nil {
					fmt.Fprintln(os.Stderr, "sweep:", err)
				} else if n > 0 {
					fmt.Printf("%d file(s) expired and were deleted.\n", n)
				}
			}
		}
	}()
	go func() {
		finder := discovery.NewFinder(self.ID(), store.Allow, 0)
		err := finder.Run(ctx, &mdns.Backend{}, &discovery.Advertisement{ID: self.ID(), Port: p})
		if err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "warning: other devices cannot find this one automatically:", err)
			fmt.Fprintln(os.Stderr, "They can still connect with -addr host:port.")
		}
	}()

	srv := &transfer.Server{
		Cert: cert, Allow: store.Allow, Shelf: sh,
		Logf: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) },
	}
	return srv.Serve(ctx, ln)
}

func addFile(sh *shelf.Shelf, path string, ttl time.Duration) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	_, err = sh.Add(f, filepath.Base(path), ttl, 0)
	return err
}

// locate finds where the paired device can be reached. If addr is given it is
// used as is. Otherwise the device is looked for on the local network.
func locate(ctx context.Context, self identity.DeviceID, store *pairing.Store, peer pairing.Peer, addr string) ([]string, error) {
	if addr != "" {
		return []string{addr}, nil
	}
	fmt.Fprintf(os.Stderr, "Looking for %q on the network...\n", peer.Name)
	finder := discovery.NewFinder(self, store.Allow, 0)
	fctx, cancel := context.WithTimeout(ctx, findTimeout)
	defer cancel()
	go finder.Run(fctx, &mdns.Backend{}, nil)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if v, ok := finder.Get(peer.ID); ok && len(v.Addrs) > 0 {
			return v.Addrs, nil
		}
		select {
		case <-fctx.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("could not find %q. Is it running 'ephdrop serve'? You can also use -addr host:port", peer.Name)
		case <-tick.C:
		}
	}
}

func prepare(dir string, fs *flag.FlagSet, args []string, need int, usage string) (*identity.Identity, *pairing.Store, pairing.Peer, []string, error) {
	if err := fs.Parse(args); err != nil {
		return nil, nil, pairing.Peer{}, nil, err
	}
	if fs.NArg() != need {
		return nil, nil, pairing.Peer{}, nil, fmt.Errorf("usage: %s", usage)
	}
	self, err := identity.LoadOrCreate(dir)
	if err != nil {
		return nil, nil, pairing.Peer{}, nil, err
	}
	store, err := openStore(dir)
	if err != nil {
		return nil, nil, pairing.Peer{}, nil, err
	}
	peer, err := matchPeer(store, fs.Arg(0))
	if err != nil {
		return nil, nil, pairing.Peer{}, nil, err
	}
	return self, store, peer, fs.Args(), nil
}

func cmdList(dir string, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	addr := fs.String("addr", "", "address of the device (host:port), skips the network search")
	self, store, peer, _, err := prepare(dir, fs, args, 1, "ephdrop list [-addr host:port] <device>")
	if err != nil {
		return err
	}
	cert, err := self.TLSCertificate()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	addrs, err := locate(ctx, self.ID(), store, peer, *addr)
	if err != nil {
		return err
	}
	cl := &transfer.Client{Cert: cert}
	entries, err := tryEach(addrs, func(a string) ([]shelf.Entry, error) { return cl.List(ctx, a, peer.ID) })
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Printf("%s is not sharing anything.\n", peer.Name)
		return nil
	}
	for _, e := range entries {
		fmt.Printf("%s  %-30s %10s  expires %s\n", e.ID, e.Name, humanSize(e.Size), e.Expires.Local().Format("Mon 15:04"))
	}
	return nil
}

func cmdGet(dir string, args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	addr := fs.String("addr", "", "address of the device (host:port), skips the network search")
	out := fs.String("o", ".", "folder to save the file in")
	// The id comes after the device, so flags and arguments may be mixed.
	args = moveFlagsFirst(args)
	self, store, peer, rest, err := prepare(dir, fs, args, 2, "ephdrop get [-addr host:port] [-o folder] <device> <file id>")
	if err != nil {
		return err
	}
	cert, err := self.TLSCertificate()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	addrs, err := locate(ctx, self.ID(), store, peer, *addr)
	if err != nil {
		return err
	}
	cl := &transfer.Client{Cert: cert}
	type result struct {
		path string
		e    shelf.Entry
	}
	r, err := tryEach(addrs, func(a string) (result, error) {
		p, e, err := cl.Pull(ctx, a, peer.ID, rest[1], *out)
		return result{p, e}, err
	})
	if err != nil {
		if errors.Is(err, shelf.ErrNotFound) {
			return errors.New("that file is not available (wrong id, or it has expired)")
		}
		return err
	}
	fmt.Printf("Saved %s (%s)\n", r.path, humanSize(r.e.Size))
	return nil
}

// moveFlagsFirst puts "-flag value" pairs before plain arguments, so
// "get <device> <id> -o dir" works as well as "get -o dir <device> <id>".
func moveFlagsFirst(args []string) []string {
	var flags, plain []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			plain = append(plain, a)
		}
	}
	return append(flags, plain...)
}

// tryEach calls f with each address until one works. Errors that are about the
// answer rather than the connection end the search.
func tryEach[T any](addrs []string, f func(addr string) (T, error)) (T, error) {
	var zero T
	var last error
	for _, a := range addrs {
		v, err := f(a)
		if err == nil {
			return v, nil
		}
		if errors.Is(err, shelf.ErrNotFound) || errors.Is(err, transfer.ErrBadFile) ||
			errors.Is(err, transfer.ErrTooLarge) || errors.Is(err, context.Canceled) {
			return zero, err
		}
		var re *transfer.RemoteError
		if errors.As(err, &re) {
			return zero, err
		}
		last = err
	}
	return zero, last
}

func humanSize(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
