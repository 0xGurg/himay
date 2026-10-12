package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"

	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/web"
)

func runWeb(ctx context.Context, st *store.Store, addr, session string, ascii, open bool) error {
	srv, err := web.New(st, web.Options{Addr: addr, ASCII: ascii})
	if err != nil {
		return err
	}
	ln, err := web.Listen(addr)
	if err != nil {
		return err
	}
	u := "http://" + ln.Addr().String() + "/"
	if session != "" {
		u += "s/" + url.PathEscape(session)
	}
	fmt.Fprintf(os.Stderr, "himay: serving %s (read-only, localhost only) — Ctrl+C to stop\n", u)
	if open {
		openBrowser(u)
	}
	return srv.Serve(ctx, ln)
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}
