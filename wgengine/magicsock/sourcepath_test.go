// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"net/netip"
	"sync"
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

func TestSourcePathSocketRxMetaConcurrentIDUpdate(t *testing.T) {
	var s sourcePathSocket
	s.setID(sourceIPv4SocketID)
	s.generation.Store(17)

	const iters = 10000
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			s.setID(sourceIPv6SocketID)
			s.setID(sourceIPv4SocketID)
		}
	}()

	for i := 0; i < iters; i++ {
		got := s.rxMeta()
		if got.generation != 17 {
			t.Fatalf("source generation = %d, want 17", got.generation)
		}
		if got.socketID != sourceIPv4SocketID && got.socketID != sourceIPv6SocketID {
			t.Fatalf("source socket ID = %d, want IPv4 or IPv6 auxiliary", got.socketID)
		}
	}
	wg.Wait()
}

func TestSourcePathProbeManagerHandlesMatchingPong(t *testing.T) {
	var pm sourcePathProbeManager
	txid := stun.NewTxID()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 7}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	src := epAddr{ap: netip.MustParseAddrPort("192.0.2.3:41641")}
	pongSrc := netip.MustParseAddrPort("198.51.100.1:41641")
	var sender key.DiscoPublic
	acceptedBefore := metricSourcePathProbePongAccepted.Value()

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
	if got := metricSourcePathProbePongAccepted.Value() - acceptedBefore; got != 1 {
		t.Fatalf("accepted pong metric delta = %d, want 1", got)
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
	expiredBefore := metricSourcePathProbePendingExpired.Value()

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
	if got := metricSourcePathProbePendingExpired.Value() - expiredBefore; got != 1 {
		t.Fatalf("expired pending metric delta = %d, want 1", got)
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
	expiredBefore := metricSourcePathProbePongExpired.Value()
	acceptedBefore := metricSourcePathProbePongAccepted.Value()

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
	if got := metricSourcePathProbePongExpired.Value() - expiredBefore; got != 1 {
		t.Fatalf("expired pong metric delta = %d, want 1", got)
	}
	if got := metricSourcePathProbePongAccepted.Value() - acceptedBefore; got != 0 {
		t.Fatalf("accepted pong metric delta after expired pong = %d, want 0", got)
	}
}

func TestSourcePathProbeManagerEnforcesPeerBudget(t *testing.T) {
	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	peerA := key.NewDisco().Public()
	peerB := key.NewDisco().Public()
	dstA := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	dstB := epAddr{ap: netip.MustParseAddrPort("192.0.2.3:41641")}
	droppedBefore := metricSourcePathProbePeerBudgetDropped.Value()

	if got := pm.addWithBudgetLocked(sourcePathProbeTx{
		txid:     stun.NewTxID(),
		dst:      dstA,
		dstDisco: peerA,
		source:   source,
		at:       now,
	}, 1, 2); got != sourcePathProbeAdded {
		t.Fatalf("first peer add result = %v, want added", got)
	}
	if got := pm.addWithBudgetLocked(sourcePathProbeTx{
		txid:     stun.NewTxID(),
		dst:      dstA,
		dstDisco: peerA,
		source:   source,
		at:       now,
	}, 1, 2); got != sourcePathProbeAdded {
		t.Fatalf("same peer add result = %v, want added", got)
	}
	if got := pm.addWithBudgetLocked(sourcePathProbeTx{
		txid:     stun.NewTxID(),
		dst:      dstB,
		dstDisco: peerB,
		source:   source,
		at:       now,
	}, 1, 2); got != sourcePathProbePeerBudgetExceeded {
		t.Fatalf("second peer add result = %v, want peer budget exceeded", got)
	}
	if got := pm.pendingLenLocked(); got != 2 {
		t.Fatalf("pending probes = %d, want 2", got)
	}
	if got := metricSourcePathProbePeerBudgetDropped.Value() - droppedBefore; got != 1 {
		t.Fatalf("peer budget metric delta = %d, want 1", got)
	}
}

func TestSourcePathProbeManagerEnforcesBurstBudget(t *testing.T) {
	var pm sourcePathProbeManager
	now := mono.Now()
	source4 := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	source6 := sourceRxMeta{socketID: sourceIPv6SocketID, generation: 6}
	peerA := key.NewDisco().Public()
	peerB := key.NewDisco().Public()
	dst4 := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	dst6 := epAddr{ap: netip.MustParseAddrPort("[2001:db8::2]:41641")}
	droppedBefore := metricSourcePathProbeBurstBudgetDropped.Value()

	if got := pm.addWithBudgetLocked(sourcePathProbeTx{
		txid:     stun.NewTxID(),
		dst:      dst4,
		dstDisco: peerA,
		source:   source4,
		at:       now,
	}, 2, 1); got != sourcePathProbeAdded {
		t.Fatalf("first probe add result = %v, want added", got)
	}
	if got := pm.addWithBudgetLocked(sourcePathProbeTx{
		txid:     stun.NewTxID(),
		dst:      dst6,
		dstDisco: peerA,
		source:   source6,
		at:       now,
	}, 2, 1); got != sourcePathProbeBurstBudgetExceeded {
		t.Fatalf("same peer burst add result = %v, want burst budget exceeded", got)
	}
	if got := pm.addWithBudgetLocked(sourcePathProbeTx{
		txid:     stun.NewTxID(),
		dst:      dst6,
		dstDisco: peerB,
		source:   source6,
		at:       now,
	}, 2, 1); got != sourcePathProbeAdded {
		t.Fatalf("second peer add result = %v, want added", got)
	}
	if got := pm.pendingLenLocked(); got != 2 {
		t.Fatalf("pending probes = %d, want 2", got)
	}
	if got := metricSourcePathProbeBurstBudgetDropped.Value() - droppedBefore; got != 1 {
		t.Fatalf("burst budget metric delta = %d, want 1", got)
	}
}

func TestSourcePathProbeManagerBestCandidateDualStackObserveOnly(t *testing.T) {
	var pm sourcePathProbeManager
	now := mono.Now()
	current4 := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 11}
	stale4 := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 10}
	current6 := sourceRxMeta{socketID: sourceIPv6SocketID, generation: 12}
	v4 := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	v4Other := epAddr{ap: netip.MustParseAddrPort("192.0.2.3:41641")}
	v6 := epAddr{ap: netip.MustParseAddrPort("[2001:db8::1]:41641")}

	pm.pending = map[stun.TxID]sourcePathProbeTx{
		stun.NewTxID(): {
			dst:    v4,
			source: current4,
			at:     now,
		},
	}
	pm.samples = []sourcePathProbeSample{
		{dst: v4, source: primarySourceRxMeta, latency: time.Millisecond, at: now.Add(-5 * time.Second)},
		{dst: v4, source: stale4, latency: time.Millisecond, at: now.Add(-4 * time.Second)},
		{dst: v4Other, source: current4, latency: time.Millisecond, at: now.Add(-3 * time.Second)},
		{dst: v4, source: current4, latency: 20 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: v4, source: current4, latency: 10 * time.Millisecond, at: now.Add(-1 * time.Second)},
		{dst: v6, source: current6, latency: 12 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: v6, source: current6, latency: 15 * time.Millisecond, at: now.Add(-1 * time.Second)},
	}
	beforePending, beforeSamples := pm.pendingLenLocked(), pm.samplesLenLocked()

	score4, ok := pm.bestCandidateLocked(v4, []sourceRxMeta{primarySourceRxMeta, current4, current6})
	if !ok {
		t.Fatal("IPv4 candidate not found")
	}
	if score4.source != current4 {
		t.Fatalf("IPv4 candidate source = %+v, want %+v", score4.source, current4)
	}
	if score4.latency != 10*time.Millisecond {
		t.Fatalf("IPv4 candidate latency = %v, want 10ms", score4.latency)
	}
	if score4.samples != 2 {
		t.Fatalf("IPv4 candidate sample count = %d, want 2", score4.samples)
	}
	if got, want := score4.lastAt.Sub(now.Add(-1*time.Second)), time.Duration(0); got != want {
		t.Fatalf("IPv4 candidate lastAt delta = %v, want %v", got, want)
	}

	score6, ok := pm.bestCandidateLocked(v6, []sourceRxMeta{current4, current6})
	if !ok {
		t.Fatal("IPv6 candidate not found")
	}
	if score6.source != current6 {
		t.Fatalf("IPv6 candidate source = %+v, want %+v", score6.source, current6)
	}
	if score6.latency != 12*time.Millisecond {
		t.Fatalf("IPv6 candidate latency = %v, want 12ms", score6.latency)
	}
	if score6.samples != 2 {
		t.Fatalf("IPv6 candidate sample count = %d, want 2", score6.samples)
	}
	if got, want := score6.lastAt.Sub(now.Add(-1*time.Second)), time.Duration(0); got != want {
		t.Fatalf("IPv6 candidate lastAt delta = %v, want %v", got, want)
	}

	if _, ok := pm.bestCandidateLocked(v4, []sourceRxMeta{primarySourceRxMeta}); ok {
		t.Fatal("primary-only source list returned a candidate")
	}
	if _, ok := pm.bestCandidateLocked(v4, []sourceRxMeta{{socketID: sourceIPv4SocketID, generation: 99}}); ok {
		t.Fatal("nonmatching generation source list returned a candidate")
	}
	if _, ok := pm.bestCandidateLocked(epAddr{}, []sourceRxMeta{current4}); ok {
		t.Fatal("non-direct destination returned a candidate")
	}
	if got := pm.pendingLenLocked(); got != beforePending {
		t.Fatalf("pending probes mutated by scoring: got %d want %d", got, beforePending)
	}
	if got := pm.samplesLenLocked(); got != beforeSamples {
		t.Fatalf("samples mutated by scoring: got %d want %d", got, beforeSamples)
	}
}

func TestReceiveIPAuxiliaryAcceptsWireGuard(t *testing.T) {
	var c Conn
	c.havePrivateKey.Store(true)
	var cache epAddrEndpointCache
	aux := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 1}
	src := netip.MustParseAddrPort("192.0.2.2:41641")

	// A non-disco, non-STUN, non-Geneve packet is classified as WireGuard.
	// msg[7] != 0 disqualifies the Geneve check so this falls through to the
	// naked-WireGuard branch in packetLooksLike.
	pkt := []byte{0x04, 0, 0, 0, 0xde, 0xad, 0xbe, 0xef}

	before := metricSourcePathAuxWireGuardRx.Value()
	ep, size, _, ok := c.receiveIPWithSource(pkt, src, &cache, aux)

	if got := metricSourcePathAuxWireGuardRx.Value() - before; got != 1 {
		t.Fatalf("aux WG rx metric delta = %d, want 1 (auxiliary WireGuard receive drop is still in place)", got)
	}
	if !ok {
		t.Fatal("aux receive returned ok=false for a WireGuard-shaped packet; the previous unconditional drop is still in effect")
	}
	if size != len(pkt) {
		t.Fatalf("size = %d, want %d", size, len(pkt))
	}
	if _, isLazy := ep.(*lazyEndpoint); !isLazy {
		t.Fatalf("returned endpoint type = %T, want *lazyEndpoint", ep)
	}
}

func TestSourcePathBestCandidateRequiresCurrentProbeSources(t *testing.T) {
	var c Conn
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 11}
	v4 := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}

	c.mu.Lock()
	c.sourceProbes.pending = map[stun.TxID]sourcePathProbeTx{
		stun.NewTxID(): {
			dst:    v4,
			source: source,
			at:     now,
		},
	}
	c.sourceProbes.samples = []sourcePathProbeSample{
		{dst: v4, source: source, latency: 10 * time.Millisecond, at: now},
	}
	beforePending, beforeSamples := c.sourceProbes.pendingLenLocked(), c.sourceProbes.samplesLenLocked()
	c.mu.Unlock()

	if _, ok := c.sourcePathBestCandidate(v4); ok {
		t.Fatal("candidate returned without current auxiliary probe sources")
	}

	c.mu.Lock()
	afterPending, afterSamples := c.sourceProbes.pendingLenLocked(), c.sourceProbes.samplesLenLocked()
	c.mu.Unlock()
	if afterPending != beforePending {
		t.Fatalf("pending probes mutated by Conn observe-only scoring: got %d want %d", afterPending, beforePending)
	}
	if afterSamples != beforeSamples {
		t.Fatalf("samples mutated by Conn observe-only scoring: got %d want %d", afterSamples, beforeSamples)
	}
}
