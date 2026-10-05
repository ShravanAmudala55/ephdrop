// Command ephdropd runs an ephdrop node and serves its local HTTP API. The
// desktop app starts it and opens the address it prints.
//
// On start it prints one line of JSON to standard output:
//
//	{"url":"http://127.0.0.1:PORT/?token=...","port":PORT,"id":"..."}
//
// Opening that URL signs a browser in. The program stops cleanly on Ctrl+C,
// SIGTERM, or when its standard input is closed (so it never outlives the
// app that started it).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ShravanAmudala55/ephdrop/core/api"
	"github.com/ShravanAmudala55/ephdrop/core/node"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ephdropd:", err)
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

func run() error {
	dir := flag.String("dir", defaultDir(), "data directory")
	name := flag.String("name", "", "name shown to other devices (default: host name)")
	downloads := flag.String("downloads", "", "default folder for downloaded files")
	listen := flag.String("listen", ":0", "address for the file transfer server")
	watchStdin := flag.Bool("exit-with-stdin", false, "stop when standard input closes")
	flag.Parse()

	n, err := node.New(node.Config{Dir: *dir, Name: *name, DownloadDir: *downloads, Listen: *listen})
	if err != nil {
		return err
	}
	srv, err := api.New(n)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *watchStdin {
		go func() {
			io.Copy(io.Discard, os.Stdin)
			stop()
		}()
	}
	if err := n.Start(ctx); err != nil {
		ln.Close()
		return err
	}
	line, _ := json.Marshal(map[string]any{
		"url":  fmt.Sprintf("http://127.0.0.1:%d/?token=%s", port, srv.Token()),
		"port": port,
		"id":   n.ID(),
	})
	fmt.Println(string(line))

	err = srv.Serve(ctx, ln)
	stop()
	n.Wait()
	return err
}
