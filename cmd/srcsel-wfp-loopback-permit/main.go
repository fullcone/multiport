// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build windows

// srcsel-wfp-loopback-permit installs a temporary high-priority Windows
// Filtering Platform permit for loopback traffic, so that the magicsock
// source-path tests can run on dev machines where another WFP callout driver
// (for example a sing-box / clash / v2ray TUN) silently drops IPv6 loopback.
//
// The session is dynamic: when this process exits the kernel removes every
// object created here, so the dev machine returns to its pre-helper state with
// no leakage even after a crash.
//
// Usage:
//
//	srcsel-wfp-loopback-permit
//
// On startup the binary prints a single line "READY pid=<pid>" to stdout, then
// blocks until stdin reaches EOF. The runner script (scripts/Run-WindowsMagic
// sockTests.ps1) reads the READY line, runs `go test`, then closes the helper
// stdin, and the kernel cleans up.
//
// Requires Administrator privileges (WFP filter manipulation needs SeLoadDriver
// equivalent rights).
package main

import (
	"fmt"
	"io"
	"log"
	"os"

	"github.com/tailscale/wf"
	"golang.org/x/sys/windows"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("srcsel-wfp-loopback-permit: ")

	sess, err := wf.New(&wf.Options{
		Name:        "srcsel test loopback permit",
		Description: "High-priority WFP permit for loopback traffic during multiport srcsel tests; dynamic session, auto-cleanup on process exit.",
		Dynamic:     true,
	})
	if err != nil {
		log.Fatalf("wf.New: %v", err)
	}
	defer sess.Close()

	pguid, err := windows.GenerateGUID()
	if err != nil {
		log.Fatalf("GenerateGUID provider: %v", err)
	}
	provID := wf.ProviderID(pguid)
	if err := sess.AddProvider(&wf.Provider{
		ID:   provID,
		Name: "srcsel test provider",
	}); err != nil {
		log.Fatalf("AddProvider: %v", err)
	}

	sguid, err := windows.GenerateGUID()
	if err != nil {
		log.Fatalf("GenerateGUID sublayer: %v", err)
	}
	subID := wf.SublayerID(sguid)
	if err := sess.AddSublayer(&wf.Sublayer{
		ID:     subID,
		Name:   "srcsel test sublayer",
		Weight: 0xFFFF,
	}); err != nil {
		log.Fatalf("AddSublayer: %v", err)
	}

	cond := []*wf.Match{
		{
			Field: wf.FieldFlags,
			Op:    wf.MatchTypeFlagsAllSet,
			Value: wf.ConditionFlagIsLoopback,
		},
	}

	layers := []wf.LayerID{
		wf.LayerALEAuthConnectV6,
		wf.LayerALEAuthRecvAcceptV6,
		wf.LayerALEAuthConnectV4,
		wf.LayerALEAuthRecvAcceptV4,
		wf.LayerInboundTransportV6,
		wf.LayerOutboundTransportV6,
		wf.LayerInboundTransportV4,
		wf.LayerOutboundTransportV4,
	}

	for _, layer := range layers {
		rguid, err := windows.GenerateGUID()
		if err != nil {
			log.Fatalf("GenerateGUID rule: %v", err)
		}
		rule := &wf.Rule{
			Name:       fmt.Sprintf("srcsel test permit loopback at %s", layer),
			ID:         wf.RuleID(rguid),
			Provider:   provID,
			Sublayer:   subID,
			Layer:      layer,
			Weight:     ^uint64(0),
			Conditions: cond,
			Action:     wf.ActionPermit,
		}
		if err := sess.AddRule(rule); err != nil {
			log.Fatalf("AddRule(%s): %v", layer, err)
		}
	}

	fmt.Printf("READY pid=%d sublayer=%v layers=%d\n", os.Getpid(), subID, len(layers))
	if err := os.Stdout.Sync(); err != nil && !isClosedFile(err) {
		log.Printf("stdout sync warning: %v", err)
	}

	io.Copy(io.Discard, os.Stdin)
	log.Println("stdin EOF, exiting; dynamic session close removes all installed filters")
}

func isClosedFile(err error) bool {
	if err == nil {
		return false
	}
	return err == os.ErrClosed
}
