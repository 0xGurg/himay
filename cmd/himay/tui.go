package main

import (
	"context"

	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/tui"
)

func runTUI(ctx context.Context, st *store.Store, session string, ascii bool) error {
	return tui.Run(ctx, st, tui.Options{SessionID: session, ASCII: ascii})
}
