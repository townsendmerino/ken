//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// watchSIGHUP runs onSignal in a goroutine each time SIGHUP arrives,
// until ctx is canceled. Standard Unix convention: SIGHUP from a
// migrate-up Makefile or `kill -HUP $(pgrep ken-mcp)` triggers a
// db.Refresher.Refresh.
//
// Per ADR-017: the Refresher's mutex serializes concurrent triggers, so
// a SIGHUP storm collapses to one refresh at a time — safe to spam.
func watchSIGHUP(ctx context.Context, onSignal func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		defer signal.Stop(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				onSignal()
			}
		}
	}()
}
