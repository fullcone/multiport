// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"net/netip"
	"testing"
	"time"

	"tailscale.com/disco"
	"tailscale.com/net/stun"
	"tailscale.com/tstime/mono"
	"tailscale.com/types/key"
)

func TestPrimarySourceRxMeta(t *testing.T) {
	if primarySourceRxMeta.socketID != primarySourceSocketID {
		t.Fatalf("primary source metadata uses socket ID %d, want %d", primarySourceRxMeta.socketID, primarySourceSocketID)
	}
	if !primarySourceRxMeta.isPrimary() {
		t.Fatal("primary source metadata is not marked primary")
	}
	if (sourceRxMeta{socketID: sourceIPv4SocketID, generation: 1}).isPrimary() {
		t.Fatal("auxiliary source metadata is marked primary")
	}
}

func TestSourcePathProbeManagerHandlesMatchingPong(t *testing.T) {
	var pm sourcePathProbeManager
	txid := stun.NewTxID()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 7}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	src := epAddr{ap: netip.MustParseAddrPort("192.0.2.3:41641")}
	pongSrc := netip.MustParseAddrPort("198.51.100.1:41641")
	var sender key.DiscoPublic

	pm.addLocked(sourcePathProbeTx{
		txid:     txid,
		dst:      dst,
		dstDisco: sender,
		source:   source,
		at:       mono.Now(),
		size:     128,
	})

	if !pm.handlePongLocked(&disco.Pong{TxID: [12]byte(txid), Src: pongSrc}, sender, src, source) {
		t.Fatal("matching auxiliary pong was not consumed")
	}
	if got := pm.pendingLenLocked(); got != 0 {
		t.Fatalf("pending probes = %d, want 0", got)
	}
	if got := pm.samplesLenLocked(); got != 1 {
		t.Fatalf("samples = %d, want 1", got)
	}
	sample := pm.samples[0]
	if sample.txid != txid {
		t.Fatalf("sample txid = %x, want %x", sample.txid, txid)
	}
	if sample.dst != dst || sample.pongFrom != src || sample.pongSrc != pongSrc || sample.source != source {
		t.Fatalf("sample = %+v, want dst=%v pongFrom=%v pongSrc=%v source=%+v", sample, dst, src, pongSrc, source)
	}
}

func TestSourcePathProbeManagerRejectsPrimaryAndMismatchedPong(t *testing.T) {
	var pm sourcePathProbeManager
	txid := stun.NewTxID()
	source := sourceRxMeta{socketID: sourceIPv6SocketID, generation: 9}
	dst := epAddr{ap: netip.MustParseAddrPort("[2001:db8::1]:41641")}
	src := epAddr{ap: netip.MustParseAddrPort("[2001:db8::2]:41641")}
	var sender key.DiscoPublic

	pm.addLocked(sourcePathProbeTx{
		txid:     txid,
		dst:      dst,
		dstDisco: sender,
		source:   source,
		at:       mono.Now(),
	})

	if pm.handlePongLocked(&disco.Pong{TxID: [12]byte(txid)}, sender, src, primarySourceRxMeta) {
		t.Fatal("primary pong consumed auxiliary probe")
	}
	if got := pm.pendingLenLocked(); got != 1 {
		t.Fatalf("pending probes after primary pong = %d, want 1", got)
	}
	if pm.handlePongLocked(&disco.Pong{TxID: [12]byte(txid)}, sender, src, sourceRxMeta{socketID: sourceIPv6SocketID, generation: 10}) {
		t.Fatal("mismatched source generation consumed auxiliary probe")
	}
	if got := pm.pendingLenLocked(); got != 1 {
		t.Fatalf("pending probes after mismatched pong = %d, want 1", got)
	}
	if !pm.handlePongLocked(&disco.Pong{TxID: [12]byte(txid)}, sender, src, source) {
		t.Fatal("matching auxiliary pong was not consumed")
	}
}

func TestSourcePathProbeManagerPrunesExpiredProbes(t *testing.T) {
	oldTimeout := pingTimeoutDuration
	pingTimeoutDuration = time.Second
	defer func() { pingTimeoutDuration = oldTimeout }()

	var pm sourcePathProbeManager
	oldTxID := stun.NewTxID()
	newTxID := stun.NewTxID()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 3}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	now := mono.Now()

	pm.addLocked(sourcePathProbeTx{
		txid:   oldTxID,
		dst:    dst,
		source: source,
		at:     now.Add(-2 * time.Second),
	})
	pm.addLocked(sourcePathProbeTx{
		txid:   newTxID,
		dst:    dst,
		source: source,
		at:     now,
	})
	if got := pm.pendingLenLocked(); got != 1 {
		t.Fatalf("pending probes after prune = %d, want 1", got)
	}
	if _, ok := pm.pending[newTxID]; !ok {
		t.Fatal("new source-path probe was pruned")
	}
	if _, ok := pm.pending[oldTxID]; ok {
		t.Fatal("expired source-path probe remains pending")
	}
}

func TestSourcePathProbeManagerConsumesExpiredPong(t *testing.T) {
	oldTimeout := pingTimeoutDuration
	pingTimeoutDuration = time.Second
	defer func() { pingTimeoutDuration = oldTimeout }()

	var pm sourcePathProbeManager
	txid := stun.NewTxID()
	source := sourceRxMeta{socketID: sourceIPv6SocketID, generation: 4}
	dst := epAddr{ap: netip.MustParseAddrPort("[2001:db8::1]:41641")}
	src := epAddr{ap: netip.MustParseAddrPort("[2001:db8::2]:41641")}
	var sender key.DiscoPublic

	pm.addLocked(sourcePathProbeTx{
		txid:     txid,
		dst:      dst,
		dstDisco: sender,
		source:   source,
		at:       mono.Now().Add(-2 * time.Second),
	})

	if !pm.handlePongLocked(&disco.Pong{TxID: [12]byte(txid)}, sender, src, source) {
		t.Fatal("expired auxiliary pong was not consumed")
	}
	if got := pm.pendingLenLocked(); got != 0 {
		t.Fatalf("pending probes after expired pong = %d, want 0", got)
	}
	if got := pm.samplesLenLocked(); got != 0 {
		t.Fatalf("samples after expired pong = %d, want 0", got)
	}
}
