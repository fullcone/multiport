// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"tailscale.com/disco"
	"tailscale.com/net/stun"
	"tailscale.com/tstest"
	"tailscale.com/tstime/mono"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
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
	}, 1, 2, 0); got != sourcePathProbeAdded {
		t.Fatalf("first peer add result = %v, want added", got)
	}
	if got := pm.addWithBudgetLocked(sourcePathProbeTx{
		txid:     stun.NewTxID(),
		dst:      dstA,
		dstDisco: peerA,
		source:   source,
		at:       now,
	}, 1, 2, 0); got != sourcePathProbeAdded {
		t.Fatalf("same peer add result = %v, want added", got)
	}
	if got := pm.addWithBudgetLocked(sourcePathProbeTx{
		txid:     stun.NewTxID(),
		dst:      dstB,
		dstDisco: peerB,
		source:   source,
		at:       now,
	}, 1, 2, 0); got != sourcePathProbePeerBudgetExceeded {
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
	}, 2, 1, 0); got != sourcePathProbeAdded {
		t.Fatalf("first probe add result = %v, want added", got)
	}
	if got := pm.addWithBudgetLocked(sourcePathProbeTx{
		txid:     stun.NewTxID(),
		dst:      dst6,
		dstDisco: peerA,
		source:   source6,
		at:       now,
	}, 2, 1, 0); got != sourcePathProbeBurstBudgetExceeded {
		t.Fatalf("same peer burst add result = %v, want burst budget exceeded", got)
	}
	if got := pm.addWithBudgetLocked(sourcePathProbeTx{
		txid:     stun.NewTxID(),
		dst:      dst6,
		dstDisco: peerB,
		source:   source6,
		at:       now,
	}, 2, 1, 0); got != sourcePathProbeAdded {
		t.Fatalf("second peer add result = %v, want added", got)
	}
	if got := pm.pendingLenLocked(); got != 2 {
		t.Fatalf("pending probes = %d, want 2", got)
	}
	if got := metricSourcePathProbeBurstBudgetDropped.Value() - droppedBefore; got != 1 {
		t.Fatalf("burst budget metric delta = %d, want 1", got)
	}
}

func TestSourcePathProbeManagerUnlimitedPeersByDefault(t *testing.T) {
	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}

	const peerCount = 100
	for i := 0; i < peerCount; i++ {
		peer := key.NewDisco().Public()
		got := pm.addWithBudgetLocked(sourcePathProbeTx{
			txid:     stun.NewTxID(),
			dst:      dst,
			dstDisco: peer,
			source:   source,
			at:       now,
		}, 0, sourcePathProbeMaxBurst, 0) // 0 peers = unlimited
		if got != sourcePathProbeAdded {
			t.Fatalf("add result for peer %d = %v, want added (peer cap should be unlimited)", i, got)
		}
	}
	if got := pm.pendingLenLocked(); got != peerCount {
		t.Fatalf("pending probes = %d, want %d", got, peerCount)
	}
}

func TestSourcePathProbeManagerEnforcesHardPendingCap(t *testing.T) {
	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	droppedBefore := metricSourcePathProbeHardCapDropped.Value()

	const hardCap = 4
	for i := 0; i < hardCap; i++ {
		peer := key.NewDisco().Public()
		got := pm.addWithBudgetLocked(sourcePathProbeTx{
			txid:     stun.NewTxID(),
			dst:      dst,
			dstDisco: peer,
			source:   source,
			at:       now,
		}, 0, sourcePathProbeMaxBurst, hardCap)
		if got != sourcePathProbeAdded {
			t.Fatalf("add %d before hard cap = %v, want added", i, got)
		}
	}
	got := pm.addWithBudgetLocked(sourcePathProbeTx{
		txid:     stun.NewTxID(),
		dst:      dst,
		dstDisco: key.NewDisco().Public(),
		source:   source,
		at:       now,
	}, 0, sourcePathProbeMaxBurst, hardCap)
	if got != sourcePathProbeHardCapExceeded {
		t.Fatalf("add at hard cap = %v, want hard cap exceeded", got)
	}
	if pm.pendingLenLocked() != hardCap {
		t.Fatalf("pending after hard cap = %d, want %d", pm.pendingLenLocked(), hardCap)
	}
	if delta := metricSourcePathProbeHardCapDropped.Value() - droppedBefore; delta != 1 {
		t.Fatalf("hard cap metric delta = %d, want 1", delta)
	}
}

func TestSourcePathProbeManagerSamplePruneOnPongAndCap(t *testing.T) {
	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}

	// Pre-populate with TTL-stale samples; pruneExpiredSamplesLocked should
	// drop them all.
	pm.samples = []sourcePathProbeSample{
		{dst: dst, source: source, latency: time.Millisecond, at: now.Add(-2 * sourcePathSampleTTL)},
		{dst: dst, source: source, latency: time.Millisecond, at: now.Add(-(sourcePathSampleTTL + time.Second))},
		{dst: dst, source: source, latency: time.Millisecond, at: now.Add(-(sourcePathSampleTTL + 2*time.Second))},
	}
	pm.pruneExpiredSamplesLocked(now)
	if got := pm.samplesLenLocked(); got != 0 {
		t.Fatalf("after prune of all-stale samples len = %d, want 0", got)
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
		{dst: v4, source: current4, latency: 30 * time.Millisecond, at: now.Add(-3 * time.Second)},
		{dst: v4, source: current4, latency: 20 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: v4, source: current4, latency: 10 * time.Millisecond, at: now.Add(-1 * time.Second)},
		{dst: v6, source: current6, latency: 18 * time.Millisecond, at: now.Add(-3 * time.Second)},
		{dst: v6, source: current6, latency: 15 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: v6, source: current6, latency: 12 * time.Millisecond, at: now.Add(-1 * time.Second)},
	}
	beforePending, beforeSamples := pm.pendingLenLocked(), pm.samplesLenLocked()

	score4, ok := pm.bestCandidateLocked(v4, []sourceRxMeta{primarySourceRxMeta, current4, current6}, now, 0)
	if !ok {
		t.Fatal("IPv4 candidate not found")
	}
	if score4.source != current4 {
		t.Fatalf("IPv4 candidate source = %+v, want %+v", score4.source, current4)
	}
	if score4.latency != 20*time.Millisecond {
		t.Fatalf("IPv4 candidate latency = %v, want 20ms (mean of 10ms, 20ms, 30ms)", score4.latency)
	}
	if score4.samples != 3 {
		t.Fatalf("IPv4 candidate sample count = %d, want 3", score4.samples)
	}
	if got, want := score4.lastAt.Sub(now.Add(-1*time.Second)), time.Duration(0); got != want {
		t.Fatalf("IPv4 candidate lastAt delta = %v, want %v", got, want)
	}

	score6, ok := pm.bestCandidateLocked(v6, []sourceRxMeta{current4, current6}, now, 0)
	if !ok {
		t.Fatal("IPv6 candidate not found")
	}
	if score6.source != current6 {
		t.Fatalf("IPv6 candidate source = %+v, want %+v", score6.source, current6)
	}
	if score6.latency != 15*time.Millisecond {
		t.Fatalf("IPv6 candidate latency = %v, want 15ms (mean of 12ms, 15ms, 18ms)", score6.latency)
	}
	if score6.samples != 3 {
		t.Fatalf("IPv6 candidate sample count = %d, want 3", score6.samples)
	}
	if got, want := score6.lastAt.Sub(now.Add(-1*time.Second)), time.Duration(0); got != want {
		t.Fatalf("IPv6 candidate lastAt delta = %v, want %v", got, want)
	}

	if _, ok := pm.bestCandidateLocked(v4, []sourceRxMeta{primarySourceRxMeta}, now, 0); ok {
		t.Fatal("primary-only source list returned a candidate")
	}
	if _, ok := pm.bestCandidateLocked(v4, []sourceRxMeta{{socketID: sourceIPv4SocketID, generation: 99}}, now, 0); ok {
		t.Fatal("nonmatching generation source list returned a candidate")
	}
	if _, ok := pm.bestCandidateLocked(epAddr{}, []sourceRxMeta{current4}, now, 0); ok {
		t.Fatal("non-direct destination returned a candidate")
	}
	if got := pm.pendingLenLocked(); got != beforePending {
		t.Fatalf("pending probes mutated by scoring: got %d want %d", got, beforePending)
	}
	if got := pm.samplesLenLocked(); got != beforeSamples {
		t.Fatalf("samples mutated by scoring: got %d want %d", got, beforeSamples)
	}
}

func TestSourcePathProbeManagerSkipsExpiredSamples(t *testing.T) {
	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 11}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}

	pm.samples = []sourcePathProbeSample{
		// Old enough to be excluded by the TTL filter.
		{dst: dst, source: source, latency: 5 * time.Millisecond, at: now.Add(-2 * sourcePathSampleTTL)},
		{dst: dst, source: source, latency: 5 * time.Millisecond, at: now.Add(-(sourcePathSampleTTL + time.Second))},
		// Fresh enough to be considered.
		{dst: dst, source: source, latency: 18 * time.Millisecond, at: now.Add(-3 * time.Second)},
		{dst: dst, source: source, latency: 22 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: dst, source: source, latency: 20 * time.Millisecond, at: now.Add(-1 * time.Second)},
	}

	score, ok := pm.bestCandidateLocked(dst, []sourceRxMeta{source}, now, 0)
	if !ok {
		t.Fatal("expected a candidate from fresh samples")
	}
	if score.samples != 3 {
		t.Fatalf("candidate sample count = %d, want 3 (expired samples excluded)", score.samples)
	}
	if score.latency != 20*time.Millisecond {
		t.Fatalf("candidate latency = %v, want 20ms (mean of fresh samples 18+22+20)", score.latency)
	}
}

func TestSourcePathProbeManagerRequiresMinSamplesForUse(t *testing.T) {
	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 7}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}

	for i := 0; i < sourcePathMinSamplesForUse-1; i++ {
		pm.samples = append(pm.samples, sourcePathProbeSample{
			dst:     dst,
			source:  source,
			latency: 10 * time.Millisecond,
			at:      now.Add(-time.Duration(i+1) * time.Second),
		})
	}
	if _, ok := pm.bestCandidateLocked(dst, []sourceRxMeta{source}, now, 0); ok {
		t.Fatalf("candidate selected with only %d fresh samples (min %d required)", sourcePathMinSamplesForUse-1, sourcePathMinSamplesForUse)
	}

	pm.samples = append(pm.samples, sourcePathProbeSample{
		dst:     dst,
		source:  source,
		latency: 10 * time.Millisecond,
		at:      now,
	})
	score, ok := pm.bestCandidateLocked(dst, []sourceRxMeta{source}, now, 0)
	if !ok {
		t.Fatalf("candidate not selected with %d fresh samples", sourcePathMinSamplesForUse)
	}
	if score.samples != sourcePathMinSamplesForUse {
		t.Fatalf("candidate sample count = %d, want %d", score.samples, sourcePathMinSamplesForUse)
	}
}

func TestSourcePathProbeManagerInvalidateDropsMatching(t *testing.T) {
	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	otherSource := sourceRxMeta{socketID: sourceIPv6SocketID, generation: 5}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	otherDst := epAddr{ap: netip.MustParseAddrPort("192.0.2.3:41641")}

	pm.samples = []sourcePathProbeSample{
		{dst: dst, source: source, latency: 10 * time.Millisecond, at: now.Add(-3 * time.Second)},
		{dst: dst, source: source, latency: 12 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: dst, source: source, latency: 14 * time.Millisecond, at: now.Add(-1 * time.Second)},
		{dst: otherDst, source: source, latency: 5 * time.Millisecond, at: now.Add(-1 * time.Second)},
		{dst: dst, source: otherSource, latency: 6 * time.Millisecond, at: now.Add(-1 * time.Second)},
	}

	dropped := pm.invalidateLocked(dst, source)
	if dropped != 3 {
		t.Fatalf("invalidateLocked dropped = %d, want 3", dropped)
	}
	if got := pm.samplesLenLocked(); got != 2 {
		t.Fatalf("samples after invalidate = %d, want 2", got)
	}
	for _, s := range pm.samples {
		if s.dst == dst && s.source == source {
			t.Fatalf("invalidated sample remained: %+v", s)
		}
	}

	if _, ok := pm.bestCandidateLocked(dst, []sourceRxMeta{source}, now, 0); ok {
		t.Fatal("invalidated (dst, source) still produces a candidate")
	}
}

func TestConnNoteSourcePathSendFailureClearsSamples(t *testing.T) {
	var c Conn
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}

	c.mu.Lock()
	c.sourceProbes.samples = []sourcePathProbeSample{
		{dst: dst, source: source, latency: 10 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: dst, source: source, latency: 12 * time.Millisecond, at: now.Add(-1 * time.Second)},
	}
	c.mu.Unlock()

	before := metricSourcePathSendFailureInvalidated.Value()
	c.noteSourcePathSendFailure(dst, source)

	c.mu.Lock()
	got := c.sourceProbes.samplesLenLocked()
	c.mu.Unlock()
	if got != 0 {
		t.Fatalf("samples after send-failure invalidation = %d, want 0", got)
	}
	if delta := metricSourcePathSendFailureInvalidated.Value() - before; delta != 2 {
		t.Fatalf("metric delta = %d, want 2", delta)
	}

	// Primary should be a no-op.
	c.mu.Lock()
	c.sourceProbes.samples = []sourcePathProbeSample{
		{dst: dst, source: source, latency: 10 * time.Millisecond, at: now},
	}
	c.mu.Unlock()
	c.noteSourcePathSendFailure(dst, primarySourceRxMeta)
	c.mu.Lock()
	remaining := c.sourceProbes.samplesLenLocked()
	c.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("primary send-failure should not touch samples; got %d remaining", remaining)
	}
}

func TestSourcePathProbeManagerPrimaryBaselineRejectsClose(t *testing.T) {
	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}

	// aux mean = 19ms, primary = 20ms → aux beats primary by only 5%, below
	// the default 10% threshold.
	for i, lat := range []time.Duration{18 * time.Millisecond, 19 * time.Millisecond, 20 * time.Millisecond} {
		pm.samples = append(pm.samples, sourcePathProbeSample{
			dst:     dst,
			source:  source,
			latency: lat,
			at:      now.Add(-time.Duration(i+1) * time.Second),
		})
	}

	rejectedBefore := metricSourcePathPrimaryBeatRejected.Value()
	primary := 20 * time.Millisecond
	if _, ok := pm.bestCandidateLocked(dst, []sourceRxMeta{source}, now, primary); ok {
		t.Fatal("aux selected despite mean (19ms) not beating primary (20ms) by 10% threshold")
	}
	if delta := metricSourcePathPrimaryBeatRejected.Value() - rejectedBefore; delta != 1 {
		t.Fatalf("primary-beat rejected metric delta = %d, want 1", delta)
	}

	// Without primary baseline (primaryRTT == 0) the same samples should
	// still allow aux selection — backward compat with Phase 19 behavior.
	score, ok := pm.bestCandidateLocked(dst, []sourceRxMeta{source}, now, 0)
	if !ok {
		t.Fatal("aux not selected when primary RTT is unknown")
	}
	if score.latency != 19*time.Millisecond {
		t.Fatalf("aux mean latency = %v, want 19ms", score.latency)
	}
}

func TestSourcePathProbeManagerPrimaryBaselineAcceptsClearWin(t *testing.T) {
	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}

	// aux mean = 5ms, primary = 20ms → aux beats primary by 75%, well above
	// the default 10% threshold.
	for i, lat := range []time.Duration{4 * time.Millisecond, 5 * time.Millisecond, 6 * time.Millisecond} {
		pm.samples = append(pm.samples, sourcePathProbeSample{
			dst:     dst,
			source:  source,
			latency: lat,
			at:      now.Add(-time.Duration(i+1) * time.Second),
		})
	}

	primary := 20 * time.Millisecond
	score, ok := pm.bestCandidateLocked(dst, []sourceRxMeta{source}, now, primary)
	if !ok {
		t.Fatal("aux not selected despite clearly beating primary")
	}
	if score.latency != 5*time.Millisecond {
		t.Fatalf("aux mean latency = %v, want 5ms", score.latency)
	}
}

func TestEndpointPrimaryRTTForLockedFallsBackToBestAddr(t *testing.T) {
	de := &endpoint{}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}

	// No state and no bestAddr → 0.
	if got := de.primaryRTTForLocked(dst); got != 0 {
		t.Fatalf("primary RTT with no data = %v, want 0", got)
	}

	// bestAddr matches dst → use bestAddr latency.
	de.bestAddr = addrQuality{epAddr: dst, latency: 17 * time.Millisecond}
	if got := de.primaryRTTForLocked(dst); got != 17*time.Millisecond {
		t.Fatalf("primary RTT from bestAddr = %v, want 17ms", got)
	}

	// endpointState entry overrides bestAddr.
	state := &endpointState{}
	state.addPongReplyLocked(pongReply{latency: 9 * time.Millisecond, pongAt: mono.Now()})
	de.endpointState = map[netip.AddrPort]*endpointState{dst.ap: state}
	if got := de.primaryRTTForLocked(dst); got != 9*time.Millisecond {
		t.Fatalf("primary RTT preferred per-address state = %v, want 9ms", got)
	}

	// Different dst with no data and bestAddr unrelated → 0.
	other := epAddr{ap: netip.MustParseAddrPort("192.0.2.99:41641")}
	if got := de.primaryRTTForLocked(other); got != 0 {
		t.Fatalf("primary RTT for unrelated dst = %v, want 0", got)
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

	primaryBefore := metricSourcePathPrimaryWireGuardRx.Value()
	before := metricSourcePathAuxWireGuardRx.Value()
	ep, size, _, ok := c.receiveIPWithSource(pkt, src, &cache, aux)

	if got := metricSourcePathPrimaryWireGuardRx.Value() - primaryBefore; got != 0 {
		t.Fatalf("primary WG rx metric delta = %d, want 0 for auxiliary receive", got)
	}
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

func TestReceiveIPPrimaryWireGuardMetric(t *testing.T) {
	var c Conn
	c.havePrivateKey.Store(true)
	var cache epAddrEndpointCache
	src := netip.MustParseAddrPort("192.0.2.2:41641")

	// A non-disco, non-STUN, non-Geneve packet is classified as WireGuard.
	pkt := []byte{0x04, 0, 0, 0, 0xde, 0xad, 0xbe, 0xef}

	primaryBefore := metricSourcePathPrimaryWireGuardRx.Value()
	auxBefore := metricSourcePathAuxWireGuardRx.Value()
	ep, size, _, ok := c.receiveIP(pkt, src, &cache)

	if got := metricSourcePathPrimaryWireGuardRx.Value() - primaryBefore; got != 1 {
		t.Fatalf("primary WG rx metric delta = %d, want 1", got)
	}
	if got := metricSourcePathAuxWireGuardRx.Value() - auxBefore; got != 0 {
		t.Fatalf("aux WG rx metric delta = %d, want 0 for primary receive", got)
	}
	if !ok {
		t.Fatal("primary receive returned ok=false for a WireGuard-shaped packet")
	}
	if size != len(pkt) {
		t.Fatalf("size = %d, want %d", size, len(pkt))
	}
	if _, isLazy := ep.(*lazyEndpoint); !isLazy {
		t.Fatalf("returned endpoint type = %T, want *lazyEndpoint", ep)
	}
}

func TestReceiveIPRemoteWireGuardMetrics(t *testing.T) {
	primary := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	aux := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:51000")}
	pkt := []byte{0x04, 0, 0, 0, 0xde, 0xad, 0xbe, 0xef}

	var c Conn
	c.havePrivateKey.Store(true)
	c.logf = logger.Discard
	c.peerMap = newPeerMap()
	peerKey := key.NewNode().Public()
	de := &endpoint{
		c:         &c,
		publicKey: peerKey,
		bestAddr:  addrQuality{epAddr: primary},
	}
	c.peerMap.byNodeKey[peerKey] = newPeerInfo(de)
	c.peerMap.setNodeKeyForEpAddr(primary, peerKey)
	c.peerMap.setNodeKeyForEpAddr(aux, peerKey)

	bestBefore := metricSourcePathRemoteBestWireGuardRx.Value()
	nonBestBefore := metricSourcePathRemoteNonBestWireGuardRx.Value()
	unknownBefore := metricSourcePathRemoteUnknownWireGuardRx.Value()
	bestAcceptedBefore := metricSourcePathRemoteBestWireGuardAccepted.Value()
	nonBestAcceptedBefore := metricSourcePathRemoteNonBestWireGuardAccepted.Value()
	path0Before := metricSourcePathRemotePath0WireGuardRx.Value()
	path1Before := metricSourcePathRemotePath1WireGuardRx.Value()
	pathOtherBefore := metricSourcePathRemotePathOtherWireGuardRx.Value()
	pathUnknownBefore := metricSourcePathRemotePathUnknownWireGuardRx.Value()
	path0AcceptedBefore := metricSourcePathRemotePath0WireGuardAccepted.Value()
	path1AcceptedBefore := metricSourcePathRemotePath1WireGuardAccepted.Value()

	var cache epAddrEndpointCache
	primaryEP, _, _, ok := c.receiveIP(pkt, primary.ap, &cache)
	if !ok {
		t.Fatal("primary endpoint receive returned ok=false")
	}
	primaryAware, ok := primaryEP.(*sourcePathPeerAwareEndpoint)
	if !ok {
		t.Fatalf("primary endpoint type = %T, want *sourcePathPeerAwareEndpoint", primaryEP)
	}
	primaryAware.FromPeer(peerKey.Raw32())
	if got := metricSourcePathRemoteBestWireGuardRx.Value() - bestBefore; got != 1 {
		t.Fatalf("remote best WG rx metric delta = %d, want 1", got)
	}
	if got := metricSourcePathRemoteBestWireGuardAccepted.Value() - bestAcceptedBefore; got != 1 {
		t.Fatalf("remote best WG accepted metric delta = %d, want 1", got)
	}
	if got := metricSourcePathRemoteNonBestWireGuardRx.Value() - nonBestBefore; got != 0 {
		t.Fatalf("remote non-best WG rx metric delta after primary = %d, want 0", got)
	}
	if got := metricSourcePathRemotePath0WireGuardRx.Value() - path0Before; got != 1 {
		t.Fatalf("remote path0 WG rx metric delta after primary = %d, want 1", got)
	}
	if got := metricSourcePathRemotePath0WireGuardAccepted.Value() - path0AcceptedBefore; got != 1 {
		t.Fatalf("remote path0 WG accepted metric delta after primary = %d, want 1", got)
	}

	cache = epAddrEndpointCache{}
	auxEP, _, _, ok := c.receiveIP(pkt, aux.ap, &cache)
	if !ok {
		t.Fatal("aux endpoint receive returned ok=false")
	}
	auxAware, ok := auxEP.(*sourcePathPeerAwareEndpoint)
	if !ok {
		t.Fatalf("aux endpoint type = %T, want *sourcePathPeerAwareEndpoint", auxEP)
	}
	auxAware.FromPeer(peerKey.Raw32())
	if got := metricSourcePathRemoteNonBestWireGuardRx.Value() - nonBestBefore; got != 1 {
		t.Fatalf("remote non-best WG rx metric delta = %d, want 1", got)
	}
	if got := metricSourcePathRemoteNonBestWireGuardAccepted.Value() - nonBestAcceptedBefore; got != 1 {
		t.Fatalf("remote non-best WG accepted metric delta = %d, want 1", got)
	}
	if got := metricSourcePathRemoteUnknownWireGuardRx.Value() - unknownBefore; got != 0 {
		t.Fatalf("remote unknown WG rx metric delta = %d, want 0", got)
	}
	if got := metricSourcePathRemotePath1WireGuardRx.Value() - path1Before; got != 1 {
		t.Fatalf("remote path1 WG rx metric delta after aux = %d, want 1", got)
	}
	if got := metricSourcePathRemotePath1WireGuardAccepted.Value() - path1AcceptedBefore; got != 1 {
		t.Fatalf("remote path1 WG accepted metric delta after aux = %d, want 1", got)
	}
	if got := metricSourcePathRemotePathOtherWireGuardRx.Value() - pathOtherBefore; got != 0 {
		t.Fatalf("remote path-other WG rx metric delta = %d, want 0", got)
	}
	if got := metricSourcePathRemotePathUnknownWireGuardRx.Value() - pathUnknownBefore; got != 0 {
		t.Fatalf("remote path-unknown WG rx metric delta = %d, want 0", got)
	}
}

func TestReceiveIPRemoteUnknownWireGuardMetric(t *testing.T) {
	var c Conn
	c.havePrivateKey.Store(true)
	c.logf = logger.Discard
	c.peerMap = newPeerMap()
	peerKey := key.NewNode().Public()
	de := &endpoint{
		c:         &c,
		publicKey: peerKey,
	}
	c.peerMap.byNodeKey[peerKey] = newPeerInfo(de)
	var cache epAddrEndpointCache
	src := netip.MustParseAddrPort("192.0.2.99:41641")
	pkt := []byte{0x04, 0, 0, 0, 0xde, 0xad, 0xbe, 0xef}

	bestBefore := metricSourcePathRemoteBestWireGuardRx.Value()
	nonBestBefore := metricSourcePathRemoteNonBestWireGuardRx.Value()
	unknownBefore := metricSourcePathRemoteUnknownWireGuardRx.Value()
	unknownAcceptedBefore := metricSourcePathRemoteUnknownWireGuardAccepted.Value()
	path0AcceptedBefore := metricSourcePathRemotePath0WireGuardAccepted.Value()
	pathUnknownBefore := metricSourcePathRemotePathUnknownWireGuardRx.Value()
	ep, _, _, ok := c.receiveIP(pkt, src, &cache)
	if !ok {
		t.Fatal("unknown endpoint receive returned ok=false")
	}
	lazy, ok := ep.(*lazyEndpoint)
	if !ok {
		t.Fatalf("unknown endpoint type = %T, want *lazyEndpoint", ep)
	}
	lazy.FromPeer(peerKey.Raw32())
	if got := metricSourcePathRemoteBestWireGuardRx.Value() - bestBefore; got != 0 {
		t.Fatalf("remote best WG rx metric delta = %d, want 0", got)
	}
	if got := metricSourcePathRemoteNonBestWireGuardRx.Value() - nonBestBefore; got != 0 {
		t.Fatalf("remote non-best WG rx metric delta = %d, want 0", got)
	}
	if got := metricSourcePathRemoteUnknownWireGuardRx.Value() - unknownBefore; got != 1 {
		t.Fatalf("remote unknown WG rx metric delta = %d, want 1", got)
	}
	if got := metricSourcePathRemoteUnknownWireGuardAccepted.Value() - unknownAcceptedBefore; got != 1 {
		t.Fatalf("remote unknown WG accepted metric delta = %d, want 1", got)
	}
	if got := metricSourcePathRemotePathUnknownWireGuardRx.Value() - pathUnknownBefore; got != 1 {
		t.Fatalf("remote path-unknown WG rx metric delta = %d, want 1", got)
	}
	if got := metricSourcePathRemotePath0WireGuardAccepted.Value() - path0AcceptedBefore; got != 1 {
		t.Fatalf("remote path0 WG accepted metric delta = %d, want 1 after peer identification", got)
	}
}

func TestClassifySourcePathRemoteSlotReplacesStaleSlot(t *testing.T) {
	now := mono.Now()
	primary := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}
	stale := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:51641")}
	fresh := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:61641")}
	ep := &endpoint{
		sourcePathRemoteSlots: [2]epAddr{primary, stale},
		sourcePathRemoteSeen:  [2]mono.Time{now, now.Add(-2 * sessionActiveTimeout)},
	}

	if got := classifySourcePathRemoteSlot(ep, fresh); got != sourcePathRemotePath1 {
		t.Fatalf("classifySourcePathRemoteSlot = %v, want path1", got)
	}
	if ep.sourcePathRemoteSlots[1] != fresh {
		t.Fatalf("slot1 = %v, want fresh source %v", ep.sourcePathRemoteSlots[1], fresh)
	}
	if ep.sourcePathRemoteSeen[1].IsZero() {
		t.Fatal("slot1 seen time was not refreshed")
	}
}

func TestClassifySourcePathRemoteSlotReplacesDeletedEndpointSlot(t *testing.T) {
	now := mono.Now()
	primary := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}
	deleted := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:51641")}
	fresh := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:61641")}
	ep := &endpoint{
		endpointState: map[netip.AddrPort]*endpointState{
			deleted.ap: {index: indexSentinelDeleted},
		},
		sourcePathRemoteSlots: [2]epAddr{primary, deleted},
		sourcePathRemoteSeen:  [2]mono.Time{now, now},
	}

	if got := classifySourcePathRemoteSlot(ep, fresh); got != sourcePathRemotePath1 {
		t.Fatalf("classifySourcePathRemoteSlot = %v, want path1", got)
	}
	if ep.sourcePathRemoteSlots[1] != fresh {
		t.Fatalf("slot1 = %v, want fresh source %v", ep.sourcePathRemoteSlots[1], fresh)
	}
}

func TestSourcePathDualSendAvailabilityLogsDegradeAndRecovery(t *testing.T) {
	var logs tstest.MemLogger
	de := &endpoint{
		c:         &Conn{logf: logs.Logf},
		publicKey: key.NewNode().Public(),
	}
	now := mono.Now()
	periodsBefore := metricSourcePathDualSendDegradedSinglePeriods.Value()
	packetsBefore := metricSourcePathDualSendDegradedSinglePackets.Value()
	recoveredBefore := metricSourcePathDualSendDegradedSingleRecovered.Value()
	durationBefore := metricSourcePathDualSendDegradedSingleDurationMS.Value()
	activeBefore := metricSourcePathDualSendDegradedSingleActivePeers.Value()

	de.noteSourcePathDualSendAvailability(now, "", "no-redundant-path", 3)
	de.noteSourcePathDualSendAvailability(now.Add(100*time.Millisecond), "", "no-redundant-path", 2)
	de.noteSourcePathDualSendAvailability(now.Add(2500*time.Millisecond), "aux-source", "", 1)

	if got := metricSourcePathDualSendDegradedSinglePeriods.Value() - periodsBefore; got != 1 {
		t.Fatalf("degraded single periods delta = %d, want 1", got)
	}
	if got := metricSourcePathDualSendDegradedSinglePackets.Value() - packetsBefore; got != 5 {
		t.Fatalf("degraded single packets delta = %d, want 5", got)
	}
	if got := metricSourcePathDualSendDegradedSingleRecovered.Value() - recoveredBefore; got != 1 {
		t.Fatalf("degraded single recovered delta = %d, want 1", got)
	}
	if got := metricSourcePathDualSendDegradedSingleDurationMS.Value() - durationBefore; got != 2500 {
		t.Fatalf("degraded single duration ms delta = %d, want 2500", got)
	}
	if got := metricSourcePathDualSendDegradedSingleActivePeers.Value() - activeBefore; got != 0 {
		t.Fatalf("degraded single active peers delta = %d, want 0", got)
	}
	logText := logs.String()
	for _, want := range []string{
		"degraded to single-send reason=no-redundant-path",
		"recovered dual-send mode=aux-source",
		"single_duration=2.5s",
		"single_packets=5",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("log output missing %q; logs:\n%s", want, logText)
		}
	}
}

func TestSourcePathPeerAwareEndpointReusedPerRemoteSource(t *testing.T) {
	ep := &endpoint{}
	src0 := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}
	src1 := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:51641")}

	wrapper0 := ep.sourcePathPeerAwareEndpoint(src0)
	if wrapper0 != ep.sourcePathPeerAwareEndpoint(src0) {
		t.Fatal("sourcePathPeerAwareEndpoint did not reuse wrapper for the same source")
	}
	if wrapper0 == ep.sourcePathPeerAwareEndpoint(src1) {
		t.Fatal("sourcePathPeerAwareEndpoint reused wrapper across different sources")
	}
}

func TestSourcePathPeerAwareEndpointCacheEvictsButKeepsPeerAware(t *testing.T) {
	ep := &endpoint{}
	for i := 0; i < sourcePathPeerAwareCacheLimit+3; i++ {
		src := epAddr{ap: netip.AddrPortFrom(netip.MustParseAddr("192.0.2.1"), uint16(40000+i))}
		wrapper := ep.sourcePathPeerAwareEndpoint(src)
		if _, ok := wrapper.(interface{ FromPeer([32]byte) }); !ok {
			t.Fatalf("wrapper for source %d is %T, want peer-aware endpoint", i, wrapper)
		}
		if len(ep.sourcePathPeerAware) > sourcePathPeerAwareCacheLimit {
			t.Fatalf("sourcePathPeerAware size = %d, want <= %d", len(ep.sourcePathPeerAware), sourcePathPeerAwareCacheLimit)
		}
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
