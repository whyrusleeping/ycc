// Command ycc-share is the public server that hosts files published by ycc daemons.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/whyrusleeping/ycc/internal/shareserver"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ycc-share:", err)
		os.Exit(1)
	}
}

func run() error {
	dataDir := flag.String("data", "", "data directory (required)")
	publicURL := flag.String("public-url", "", "externally visible base URL used to build links (required)")
	tokenFile := flag.String("token-file", "", "file holding the upload token (default: $YCC_SHARE_TOKEN)")
	listen := flag.String("listen", "127.0.0.1:8095", "listen address")
	ttl := flag.Duration("ttl", 720*time.Hour, "lifetime of each upload; 0 keeps uploads until deleted")
	maxBytes := byteSize(50 << 20)
	flag.Var(&maxBytes, "max-bytes", "maximum size of one upload, in bytes or with a suffix such as 50MiB")
	flag.Parse()
	if flag.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", flag.Args())
	}
	if *dataDir == "" || *publicURL == "" {
		return errors.New("--data and --public-url are required")
	}

	token := os.Getenv("YCC_SHARE_TOKEN")
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		token = string(b)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("no upload token: set --token-file or YCC_SHARE_TOKEN")
	}

	srv, err := shareserver.New(shareserver.Config{
		Dir:       *dataDir,
		PublicURL: *publicURL,
		Token:     token,
		TTL:       *ttl,
		MaxBytes:  int64(maxBytes),
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.Run(ctx)

	hs := &http.Server{
		Addr:              *listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Printf("ycc-share: listening on %s, serving %s", *listen, *publicURL)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Printf("ycc-share: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hs.Shutdown(shutdownCtx)
}

// byteSize is a flag value accepting plain bytes or a size with a unit suffix.
type byteSize int64

var units = []struct {
	suffix string
	mult   int64
}{
	{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30},
	{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9},
	{"B", 1},
}

func (b *byteSize) String() string { return strconv.FormatInt(int64(*b), 10) }

func (b *byteSize) Set(s string) error {
	num, mult := strings.TrimSpace(s), int64(1)
	for _, u := range units {
		if n, ok := strings.CutSuffix(num, u.suffix); ok {
			num, mult = strings.TrimSpace(n), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n <= 0 || n > (1<<62)/mult {
		return fmt.Errorf("invalid size %q", s)
	}
	*b = byteSize(n * mult)
	return nil
}
