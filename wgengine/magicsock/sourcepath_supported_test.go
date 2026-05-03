// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux || windows

package magicsock

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tailscale/wireguard-go/device"
	"github.com/tailscale/wireguard-go/tun/tuntest"
	"tailscale.com/envknob"
	"tailscale.com/net/packet"
	"tailscale.com/net/stun"
	"tailscale.com/tstime/mono"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
	"tailscale.com/types/netmap"
	"tailscale.com/types/nettype"
)

// TestSourcePathProbeManagerPrimaryBaselineThresholdEnvOverride and
// TestSourcePathProbeManagerPrimaryBaselineThresholdEnvClampedTo100 live here
// (rather than sourcepath_test.go) because they assume
// TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT is honored via
// sourcePathAuxBeatThresholdPercentValue. On non-(linux||windows) builds the
// stub in sourcepath_default.go always returns the constant default and
// ignores the env knob, which would make these assertions fail
// deterministically.
func TestSourcePathProbeManagerPrimaryBaselineThresholdEnvOverride(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT", "1")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT", "") })

	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}

	// aux mean = 19ms, primary = 20ms → 5% improvement. With threshold
	// lowered to 1% via env, aux now qualifies.
	for i, lat := range []time.Duration{18 * time.Millisecond, 19 * time.Millisecond, 20 * time.Millisecond} {
		pm.samples = append(pm.samples, sourcePathProbeSample{
			dst:     dst,
			source:  source,
			latency: lat,
			at:      now.Add(-time.Duration(i+1) * time.Second),
		})
	}

	primary := 20 * time.Millisecond
	if _, ok := pm.bestCandidateLocked(dst, []sourceRxMeta{source}, now, primary); !ok {
		t.Fatal("aux not selected with 1%% threshold env override despite 5%% improvement")
	}
}

func TestSourcePathProbeManagerPrimaryBaselineThresholdEnvClampedTo100(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT", "200")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT", "") })

	if got := sourcePathAuxBeatThresholdPercentValue(); got != 100 {
		t.Fatalf("threshold value = %d, want 100 (clamped)", got)
	}

	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}

	// Even an aux mean of 1ms cannot beat primary RTT of 20ms × 0% = 0ms.
	for i, lat := range []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond} {
		pm.samples = append(pm.samples, sourcePathProbeSample{
			dst:     dst,
			source:  source,
			latency: lat,
			at:      now.Add(-time.Duration(i+1) * time.Second),
		})
	}

	primary := 20 * time.Millisecond
	if _, ok := pm.bestCandidateLocked(dst, []sourceRxMeta{source}, now, primary); ok {
		t.Fatal("aux selected with 100%% threshold (no aux should ever beat primary)")
	}
}

func TestSourcePathProbeManagerMultiMetricPrefersLowJitter(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_MULTI_METRIC", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_SCORE_WEIGHTS", "lat=0.1,jit=0.8,loss=0.1")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_MULTI_METRIC", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_SCORE_WEIGHTS", "")
	})

	var pm sourcePathProbeManager
	now := mono.Now()
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	jittery := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	stable := sourceRxMeta{socketID: sourceIPv6SocketID, generation: 5}

	for i, lat := range []time.Duration{1 * time.Millisecond, 20 * time.Millisecond, 39 * time.Millisecond} {
		pm.samples = append(pm.samples, sourcePathProbeSample{
			dst:     dst,
			source:  jittery,
			latency: lat,
			at:      now.Add(-time.Duration(i+1) * time.Second),
		})
	}
	for i := 0; i < sourcePathMinSamplesForUse; i++ {
		pm.samples = append(pm.samples, sourcePathProbeSample{
			dst:     dst,
			source:  stable,
			latency: 25 * time.Millisecond,
			at:      now.Add(-time.Duration(i+1) * time.Second),
		})
	}

	score, ok := pm.bestCandidateLocked(dst, []sourceRxMeta{jittery, stable}, now, 0)
	if !ok {
		t.Fatal("expected multi-metric candidate")
	}
	if score.source != stable {
		t.Fatalf("multi-metric selected source %+v, want stable source %+v", score.source, stable)
	}
	if score.jitter != 0 {
		t.Fatalf("stable source jitter = %v, want 0", score.jitter)
	}
}

func TestSourcePathProbeManagerMultiMetricHardAvoidLoss(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_MULTI_METRIC", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_LOSS_MAX_PCT", "5")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_MULTI_METRIC", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_LOSS_MAX_PCT", "")
	})

	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	for i := 0; i < sourcePathMinSamplesForUse; i++ {
		pm.samples = append(pm.samples, sourcePathProbeSample{
			dst:     dst,
			source:  source,
			latency: 10 * time.Millisecond,
			at:      now.Add(-time.Duration(i+1) * time.Second),
		})
	}
	for i := 0; i < 10; i++ {
		pm.noteOutcomeLocked(dst, source, now.Add(-time.Duration(i)*time.Second), i < 1)
	}

	before := metricSourcePathHardAvoidLoss.Value()
	if _, ok := pm.bestCandidateLocked(dst, []sourceRxMeta{source}, now, 0); ok {
		t.Fatal("candidate selected despite loss above hard-avoid threshold")
	}
	if delta := metricSourcePathHardAvoidLoss.Value() - before; delta != 1 {
		t.Fatalf("hard-avoid-loss metric delta = %d, want 1", delta)
	}
}

func TestSourcePathProbeManagerOutcomeHardCap(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_MAX_OUTCOMES", "3")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_MAX_OUTCOMES", "") })

	var pm sourcePathProbeManager
	now := mono.Now()
	source := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 5}
	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	for i := 0; i < 5; i++ {
		pm.noteOutcomeLocked(dst, source, now.Add(time.Duration(i)*time.Millisecond), i%2 == 0)
	}

	if got := len(pm.outcomes); got != 3 {
		t.Fatalf("outcome count = %d, want capped count 3", got)
	}
	for i, outcome := range pm.outcomes {
		wantAt := now.Add(time.Duration(i+2) * time.Millisecond)
		if outcome.at != wantAt {
			t.Fatalf("outcome[%d].at = %v, want %v", i, outcome.at, wantAt)
		}
	}
}

func TestSourcePathActiveBackupFailoverAndRecovery(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "active-backup")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ACTIVE_BACKUP", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PRIMARY_FAIL_STREAK", "2")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FAILOVER_RECOVERY_PONGS", "2")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ACTIVE_BACKUP", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PRIMARY_FAIL_STREAK", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FAILOVER_RECOVERY_PONGS", "")
	})

	var c Conn
	c.peerMap = newPeerMap()
	c.sourcePath.generation = 25
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true

	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	aux := c.sourcePath.aux4.rxMeta()
	now := mono.Now()
	c.mu.Lock()
	c.sourceProbes.samples = []sourcePathProbeSample{
		{dst: dst, source: aux, latency: 10 * time.Millisecond, at: now.Add(-3 * time.Second)},
		{dst: dst, source: aux, latency: 11 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: dst, source: aux, latency: 9 * time.Millisecond, at: now.Add(-1 * time.Second)},
	}
	c.mu.Unlock()

	if source, ok := c.noteSourcePathPrimarySendFailure(dst, now); ok {
		t.Fatalf("first primary failure selected failover source %+v, want no failover yet", source)
	}
	source, ok := c.noteSourcePathPrimarySendFailure(dst, now.Add(time.Millisecond))
	if !ok {
		t.Fatal("second primary failure did not select failover source")
	}
	if source != aux {
		t.Fatalf("failover source = %+v, want %+v", source, aux)
	}
	if got := c.sourcePathDataSendSource(dst); got != aux {
		t.Fatalf("active-backup data source = %+v, want forced aux %+v", got, aux)
	}

	peerKey := key.NewNode().Public()
	state := &endpointState{}
	state.addPongReplyLocked(pongReply{latency: 7 * time.Millisecond, pongAt: now.Add(2 * time.Millisecond)})
	state.addPongReplyLocked(pongReply{latency: 8 * time.Millisecond, pongAt: now.Add(3 * time.Millisecond)})
	de := &endpoint{
		c:             &c,
		publicKey:     peerKey,
		endpointState: map[netip.AddrPort]*endpointState{dst.ap: state},
	}
	c.mu.Lock()
	c.peerMap.byNodeKey[peerKey] = newPeerInfo(de)
	c.peerMap.setNodeKeyForEpAddr(dst, peerKey)
	c.mu.Unlock()

	if got := c.sourcePathDataSendSource(dst); !got.isPrimary() {
		t.Fatalf("active-backup source after recovery pongs = %+v, want primary", got)
	}
}

func TestSourcePathActiveBackupDropsStaleFailoverSource(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "active-backup")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ACTIVE_BACKUP", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PRIMARY_FAIL_STREAK", "1")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ACTIVE_BACKUP", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PRIMARY_FAIL_STREAK", "")
	})

	var c Conn
	c.sourcePath.generation = 31
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true

	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	oldAux := c.sourcePath.aux4.rxMeta()
	now := mono.Now()
	c.mu.Lock()
	c.sourceProbes.samples = []sourcePathProbeSample{
		{dst: dst, source: oldAux, latency: 10 * time.Millisecond, at: now.Add(-3 * time.Second)},
		{dst: dst, source: oldAux, latency: 11 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: dst, source: oldAux, latency: 9 * time.Millisecond, at: now.Add(-1 * time.Second)},
	}
	c.mu.Unlock()
	if _, ok := c.noteSourcePathPrimarySendFailure(dst, now); !ok {
		t.Fatal("primary failure did not select initial failover source")
	}

	c.sourcePath.generation++
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))

	if got := c.sourcePathDataSendSource(dst); !got.isPrimary() {
		t.Fatalf("active-backup source after aux generation change = %+v, want primary", got)
	}
	c.mu.Lock()
	_, stillForced := c.sourceProbes.activeBackup[dst]
	c.mu.Unlock()
	if stillForced {
		t.Fatal("active-backup state still forced after cached aux source became stale")
	}
}

func TestSourcePathFlowAwareRRStickyAndIdle(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY", "rr")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_IDLE_S", "1")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_IDLE_S", "")
	})

	var c Conn
	c.sourcePath.generation = 32
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true

	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	aux := c.sourcePath.aux4.rxMeta()
	now := mono.Now()
	seedSourcePathSamples(t, &c, dst, aux)

	if got := c.sourcePathDataSendSourceForFlow(dst, 1, now); !got.isPrimary() {
		t.Fatalf("first RR flow source = %+v, want primary", got)
	}
	if got := c.sourcePathDataSendSourceForFlow(dst, 2, now.Add(time.Millisecond)); got != aux {
		t.Fatalf("second RR flow source = %+v, want aux %+v", got, aux)
	}
	if got := c.sourcePathDataSendSourceForFlow(dst, 2, now.Add(2*time.Millisecond)); got != aux {
		t.Fatalf("sticky RR flow source = %+v, want aux %+v", got, aux)
	}
	if got := c.sourcePathDataSendSourceForFlow(dst, 2, now.Add(2*time.Second)); !got.isPrimary() {
		t.Fatalf("expired RR flow source = %+v, want primary after idle remap", got)
	}
}

func TestSourcePathFlowAwareRRIndependentPerDestination(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY", "rr")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY", "")
	})

	var c Conn
	c.sourcePath.generation = 36
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true

	dst1 := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	dst2 := epAddr{ap: netip.MustParseAddrPort("192.0.2.3:41641")}
	aux := c.sourcePath.aux4.rxMeta()
	now := mono.Now()
	seedSourcePathSamples(t, &c, dst1, aux)
	seedSourcePathSamples(t, &c, dst2, aux)

	if got := c.sourcePathDataSendSourceForFlow(dst1, 1, now); !got.isPrimary() {
		t.Fatalf("first dst1 RR flow source = %+v, want primary", got)
	}
	if got := c.sourcePathDataSendSourceForFlow(dst2, 1, now.Add(time.Millisecond)); !got.isPrimary() {
		t.Fatalf("first dst2 RR flow source = %+v, want independent primary", got)
	}
	if got := c.sourcePathDataSendSourceForFlow(dst1, 2, now.Add(2*time.Millisecond)); got != aux {
		t.Fatalf("second dst1 RR flow source = %+v, want aux %+v", got, aux)
	}
	if got := c.sourcePathDataSendSourceForFlow(dst2, 2, now.Add(3*time.Millisecond)); got != aux {
		t.Fatalf("second dst2 RR flow source = %+v, want aux %+v", got, aux)
	}
}

func TestSourcePathFlowAwareDropsStaleAssignedSource(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY", "rr")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY", "")
	})

	var c Conn
	c.sourcePath.generation = 33
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true

	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	aux := c.sourcePath.aux4.rxMeta()
	now := mono.Now()
	seedSourcePathSamples(t, &c, dst, aux)

	if got := c.sourcePathDataSendSourceForFlow(dst, 1, now); !got.isPrimary() {
		t.Fatalf("first RR flow source = %+v, want primary", got)
	}
	if got := c.sourcePathDataSendSourceForFlow(dst, 2, now.Add(time.Millisecond)); got != aux {
		t.Fatalf("second RR flow source = %+v, want aux %+v", got, aux)
	}

	c.sourcePath.generation++
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))

	if got := c.sourcePathDataSendSourceForFlow(dst, 2, now.Add(2*time.Millisecond)); !got.isPrimary() {
		t.Fatalf("stale flow source after aux generation change = %+v, want primary", got)
	}
	c.mu.Lock()
	mapped := c.sourceProbes.flowMap[sourcePathFlowKey{dst: dst, id: 2}]
	c.mu.Unlock()
	if !mapped.source.isPrimary() {
		t.Fatalf("flow map source after stale aux remap = %+v, want primary", mapped.source)
	}
}

func TestSourcePathFlowAwareHardCap(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY", "rr")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_MAX", "1")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_MAX", "")
	})

	var c Conn
	c.sourcePath.generation = 34
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true

	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	seedSourcePathSamples(t, &c, dst, c.sourcePath.aux4.rxMeta())
	now := mono.Now()
	c.sourcePathDataSendSourceForFlow(dst, 1, now)
	c.sourcePathDataSendSourceForFlow(dst, 2, now.Add(time.Millisecond))

	c.mu.Lock()
	got := len(c.sourceProbes.flowMap)
	c.mu.Unlock()
	if got != 1 {
		t.Fatalf("flow map size = %d, want hard cap 1", got)
	}
}

func TestSourcePathFlowAwareBatchFallbackStripesTransportPackets(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY", "rr")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY", "")
	})

	var c Conn
	c.sourcePath.generation = 35
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true

	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	aux := c.sourcePath.aux4.rxMeta()
	seedSourcePathSamples(t, &c, dst, aux)

	pkt1 := sourcePathTestTransportPacket(123, 1)
	pkt2 := sourcePathTestTransportPacket(123, 2)
	if got := c.sourcePathDataSendSourceForBatch(dst, [][]byte{pkt1}, 0); !got.isPrimary() {
		t.Fatalf("first packet-fallback source = %+v, want primary", got)
	}
	if got := c.sourcePathDataSendSourceForBatch(dst, [][]byte{pkt2}, 0); got != aux {
		t.Fatalf("second packet-fallback source = %+v, want aux %+v", got, aux)
	}
}

func TestSourcePathFlowIDFromTransportPacketIncludesCounter(t *testing.T) {
	pkt1 := sourcePathTestTransportPacket(123, 1)
	pkt2 := sourcePathTestTransportPacket(123, 2)

	id1, ok1 := sourcePathFlowIDFromPacket(pkt1)
	id2, ok2 := sourcePathFlowIDFromPacket(pkt2)
	if !ok1 || !ok2 {
		t.Fatal("transport packet did not produce a flow ID")
	}
	if id1 == id2 {
		t.Fatalf("transport flow IDs match despite different counters: %x", id1)
	}
}

func sourcePathTestTransportPacket(receiver uint32, counter uint64) []byte {
	pkt := make([]byte, 32)
	binary.LittleEndian.PutUint32(pkt[:4], device.MessageTransportType)
	binary.LittleEndian.PutUint32(pkt[4:8], receiver)
	binary.LittleEndian.PutUint64(pkt[8:16], counter)
	copy(pkt[16:], "ciphertext")
	return pkt
}

func TestSourcePathRealtimeProfileValues(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PROFILE", "realtime")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PROFILE", "") })

	if !sourcePathMultiMetricEnabled() {
		t.Fatal("realtime profile did not enable multi-metric scoring")
	}
	if got := sourcePathProbeIntervalValue(); got != sourcePathRealtimeProbeEvery {
		t.Fatalf("realtime probe interval = %v, want %v", got, sourcePathRealtimeProbeEvery)
	}
	if got := sourcePathSampleTTLValue(); got != sourcePathRealtimeSampleTTL {
		t.Fatalf("realtime sample TTL = %v, want %v", got, sourcePathRealtimeSampleTTL)
	}
}

func TestSourcePathProbeIntervalDefaultAndOptOut(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PROFILE", "")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PROBE_INTERVAL_MS", "")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PROFILE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PROBE_INTERVAL_MS", "")
	})

	if got := sourcePathProbeIntervalValue(); got != sourcePathRealtimeProbeEvery {
		t.Fatalf("default probe interval = %v, want %v", got, sourcePathRealtimeProbeEvery)
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PROBE_INTERVAL_MS", "0")
	if got := sourcePathProbeIntervalValue(); got != 0 {
		t.Fatalf("explicit zero probe interval = %v, want disabled", got)
	}
}

func TestSourcePathProbeIntervalFloor(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PROBE_INTERVAL_MS", "1")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PROBE_INTERVAL_MS", "") })

	if got := sourcePathProbeIntervalValue(); got != sourcePathProbeIntervalFloor {
		t.Fatalf("probe interval = %v, want floor %v", got, sourcePathProbeIntervalFloor)
	}
}

func TestSourcePathProbeBurstDefaultScalesWithAuxSocketCount(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", strconv.Itoa(sourcePathMaxAuxSockets))
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST", "")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST", "")
	})

	if got, want := sourcePathProbeMaxBurstCount(), sourcePathMaxAuxSockets*2; got != want {
		t.Fatalf("default probe burst = %d, want %d for %d aux sockets", got, want, sourcePathMaxAuxSockets)
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST", "7")
	if got := sourcePathProbeMaxBurstCount(); got != 7 {
		t.Fatalf("explicit probe burst = %d, want 7", got)
	}
}

func TestSourcePathAuxSocketCountBoundaryDualStack(t *testing.T) {
	tests := []struct {
		name    string
		enable  string
		aux     string
		wantAux int
	}{
		{
			name:    "default",
			enable:  "",
			aux:     "",
			wantAux: sourcePathDefaultAuxSockets,
		},
		{
			name:    "default-explicit-one",
			enable:  "",
			aux:     "1",
			wantAux: 1,
		},
		{
			name:    "disabled",
			enable:  "false",
			aux:     "1",
			wantAux: 0,
		},
		{
			name:    "zero",
			enable:  "true",
			aux:     "0",
			wantAux: 0,
		},
		{
			name:    "negative",
			enable:  "true",
			aux:     "-1",
			wantAux: 0,
		},
		{
			name:    "one",
			enable:  "true",
			aux:     "1",
			wantAux: 1,
		},
		{
			name:    "more-than-one",
			enable:  "true",
			aux:     "2",
			wantAux: 2,
		},
		{
			name:    "max-clamps",
			enable:  "true",
			aux:     strconv.Itoa(sourcePathMaxAuxSockets + 1),
			wantAux: sourcePathMaxAuxSockets,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", tt.enable)
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", tt.aux)
			t.Cleanup(func() {
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
			})

			if got := sourcePathAuxSocketCount(); got != tt.wantAux {
				t.Fatalf("sourcePathAuxSocketCount() = %d, want %d", got, tt.wantAux)
			}
		})
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "2")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
	})

	var c Conn
	c.sourcePath.generation = 3
	c.sourcePath.mu.Lock()
	c.ensureSourcePathAuxSocketCountLocked(2)
	c.forEachSourcePathSocketLocked(true, func(_ int, sock *sourcePathSocket, bound *bool) {
		sock.generation.Store(uint64(c.sourcePath.generation))
		*bound = true
	})
	c.forEachSourcePathSocketLocked(false, func(_ int, sock *sourcePathSocket, bound *bool) {
		sock.generation.Store(uint64(c.sourcePath.generation))
		*bound = true
	})
	c.sourcePath.mu.Unlock()

	sources4 := c.sourcePathProbeSources(true)
	sources6 := c.sourcePathProbeSources(false)
	if len(sources4) != 2 || sources4[0].socketID != sourceIPv4SocketID || sources4[1].socketID != sourceIPv4ExtraSocketIDBase {
		t.Fatalf("IPv4 probe sources with AUX_SOCKETS=2 = %+v, want two IPv4 auxiliary sources", sources4)
	}
	if len(sources6) != 2 || sources6[0].socketID != sourceIPv6SocketID || sources6[1].socketID != sourceIPv6ExtraSocketIDBase {
		t.Fatalf("IPv6 probe sources with AUX_SOCKETS=2 = %+v, want two IPv6 auxiliary sources", sources6)
	}
}

func TestSourcePathDualSendDefaultAndOptOut(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
	})

	if !sourcePathEnabled() {
		t.Fatal("source path disabled by default")
	}
	if got := sourcePathAuxSocketCount(); got != sourcePathDefaultAuxSockets {
		t.Fatalf("default sourcePathAuxSocketCount() = %d, want %d", got, sourcePathDefaultAuxSockets)
	}
	if !sourcePathDualSendEnabled() {
		t.Fatal("dual-send disabled by default")
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
	if sourcePathDualSendEnabled() {
		t.Fatal("dual-send enabled in single-source strategy mode")
	}
	if !sourcePathSingleSourceStrategyEnabled() {
		t.Fatal("single-source strategy mode was not enabled")
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "active-backup")
	if sourcePathDualSendEnabled() {
		t.Fatal("dual-send enabled in active-backup strategy mode")
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "dual-endpoint")
	if sourcePathDualSendEnabled() {
		t.Fatal("dual-send enabled in dual-endpoint strategy mode")
	}
	if !sourcePathDualEndpointStrategyEnabled() {
		t.Fatal("dual-endpoint strategy mode was not enabled")
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "false")
	if sourcePathDualSendEnabled() {
		t.Fatal("dual-send remained enabled with TS_EXPERIMENTAL_SRCSEL_DUAL_SEND=false")
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "0")
	if sourcePathDualSendEnabled() {
		t.Fatal("dual-send enabled with TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=0")
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "false")
	if sourcePathAuxSocketCount() != 0 {
		t.Fatal("source path aux sockets remained enabled with TS_EXPERIMENTAL_SRCSEL_ENABLE=false")
	}
	if sourcePathDualSendEnabled() {
		t.Fatal("dual-send enabled with TS_EXPERIMENTAL_SRCSEL_ENABLE=false")
	}
}

func TestSourcePathDataStrategyDefaultSuppressesSingleSourceControls(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY", "rr")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY", "")
	})

	var c Conn
	c.sourcePath.generation = 8
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true

	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}
	seedSourcePathSamples(t, &c, dst, c.sourcePath.aux4.rxMeta())

	if got := sourcePathDataStrategyMode(); got != sourcePathDataStrategyDualSend {
		t.Fatalf("default data strategy = %q, want %q", got, sourcePathDataStrategyDualSend)
	}
	if sourcePathSingleSourceStrategyEnabled() {
		t.Fatal("single-source strategy enabled by default")
	}
	if sourcePathFlowAwareEnabled() {
		t.Fatal("flow-aware single-source mode enabled by default")
	}
	if got := c.sourcePathDataSendSource(dst); !got.isPrimary() {
		t.Fatalf("default strategy honored force/auto source = %+v, want primary", got)
	}
	if got := c.sourcePathDataSendSourceForBatch(dst, [][]byte{sourcePathTestTransportPacket(123, 1)}, 0); !got.isPrimary() {
		t.Fatalf("default strategy honored flow-aware source = %+v, want primary", got)
	}
}

func TestSourcePathDualEndpointStrategySendsTwoLowestLatencyEndpoints(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "dual-endpoint")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
	})

	dstFast := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	dstMid := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	dstSlow := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	primaryConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")

	var c Conn
	c.metrics = new(metrics)
	c.logf = func(string, ...any) {}
	c.pconn4.mu.Lock()
	c.pconn4.setConnLocked(primaryConn, "udp4", 1)
	c.pconn4.mu.Unlock()

	now := mono.Now()
	fast := udpConnAddrPort(t, dstFast.LocalAddr())
	mid := udpConnAddrPort(t, dstMid.LocalAddr())
	slow := udpConnAddrPort(t, dstSlow.LocalAddr())
	fastState := &endpointState{}
	fastState.addPongReplyLocked(pongReply{latency: 10 * time.Millisecond, pongAt: now})
	midState := &endpointState{}
	midState.addPongReplyLocked(pongReply{latency: 20 * time.Millisecond, pongAt: now})
	slowState := &endpointState{}
	slowState.addPongReplyLocked(pongReply{latency: 50 * time.Millisecond, pongAt: now})

	de := &endpoint{
		c:                 &c,
		heartbeatDisabled: true,
		endpointState: map[netip.AddrPort]*endpointState{
			slow: slowState,
			mid:  midState,
			fast: fastState,
		},
	}

	de.mu.Lock()
	addrs, _ := de.dualEndpointAddrsForSendLocked(now)
	de.mu.Unlock()
	if len(addrs) != 2 {
		t.Fatalf("dual endpoint candidates = %+v, want two endpoints", addrs)
	}
	if addrs[0].ap != fast || addrs[1].ap != mid {
		t.Fatalf("dual endpoint candidates = %+v, want fast %v then mid %v", addrs, fast, mid)
	}

	payload := sourcePathTestTransportPacket(101, 1)
	buf := make([]byte, packet.GeneveFixedHeaderLength+len(payload))
	copy(buf[packet.GeneveFixedHeaderLength:], payload)
	before := metricSourcePathDualEndpointPackets.Value()
	if err := c.Send([][]byte{buf}, de, packet.GeneveFixedHeaderLength); err != nil {
		t.Fatalf("Conn.Send returned error: %v", err)
	}
	if got := metricSourcePathDualEndpointPackets.Value() - before; got != 1 {
		t.Fatalf("dual-endpoint packets metric delta = %d, want 1", got)
	}

	readUDPConnPayload(t, dstFast, payload)
	readUDPConnPayload(t, dstMid, payload)
	assertNoUDPConnPayload(t, dstSlow)
}

func TestSourcePathDualSendUsesObservedRemoteEndpoints(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
	})

	primaryDst := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	secondaryDst := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	primaryConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	auxConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")

	var c Conn
	c.metrics = new(metrics)
	c.logf = func(string, ...any) {}
	c.sourcePath.generation = 29
	c.pconn4.mu.Lock()
	c.pconn4.setConnLocked(primaryConn, "udp4", 1)
	c.pconn4.mu.Unlock()
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4.pconn.mu.Lock()
	c.sourcePath.aux4.pconn.setConnLocked(auxConn, "udp4", 1)
	c.sourcePath.aux4.pconn.mu.Unlock()
	c.sourcePath.aux4Bound = true

	primary := epAddr{ap: udpConnAddrPort(t, primaryDst.LocalAddr())}
	secondary := epAddr{ap: udpConnAddrPort(t, secondaryDst.LocalAddr())}
	now := mono.Now()
	de := &endpoint{
		c:                     &c,
		heartbeatDisabled:     true,
		bestAddr:              addrQuality{epAddr: primary},
		trustBestAddrUntil:    now.Add(time.Hour),
		sourcePathRemoteSlots: [2]epAddr{primary, secondary},
		sourcePathRemoteSeen:  [2]mono.Time{now, now},
	}

	payload := sourcePathTestTransportPacket(102, 1)
	buf := make([]byte, packet.GeneveFixedHeaderLength+len(payload))
	copy(buf[packet.GeneveFixedHeaderLength:], payload)
	before := metricSourcePathDualSendPackets.Value()
	if err := c.Send([][]byte{buf}, de, packet.GeneveFixedHeaderLength); err != nil {
		t.Fatalf("Conn.Send returned error: %v", err)
	}
	if got := metricSourcePathDualSendPackets.Value() - before; got != 1 {
		t.Fatalf("dual-send packets metric delta = %d, want 1", got)
	}

	wantPrimarySrc := udpConnAddrPort(t, primaryConn.LocalAddr())
	for name, conn := range map[string]*net.UDPConn{
		"primary":   primaryDst,
		"secondary": secondaryDst,
	} {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 128)
		n, src, err := conn.ReadFromUDPAddrPort(got)
		if err != nil {
			t.Fatalf("%s ReadFromUDPAddrPort: %v", name, err)
		}
		if string(got[:n]) != string(payload) {
			t.Fatalf("%s received payload %q, want %q", name, got[:n], payload)
		}
		if src.Port() != wantPrimarySrc.Port() {
			t.Fatalf("%s source port = %d, want primary port %d", name, src.Port(), wantPrimarySrc.Port())
		}
	}
}

func TestSourcePathDualSendPathPoolChoosesBestSourcePerEndpoint(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "2")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
	})

	dstA := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	dstB := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	dstStandby := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	primaryConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	aux1Conn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	aux2Conn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")

	var c Conn
	c.metrics = new(metrics)
	c.logf = func(string, ...any) {}
	c.sourcePath.generation = 39
	c.pconn4.mu.Lock()
	c.pconn4.setConnLocked(primaryConn, "udp4", 1)
	c.pconn4.mu.Unlock()
	c.sourcePath.mu.Lock()
	c.ensureSourcePathAuxSocketCountLocked(2)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4.pconn.mu.Lock()
	c.sourcePath.aux4.pconn.setConnLocked(aux1Conn, "udp4", 1)
	c.sourcePath.aux4.pconn.mu.Unlock()
	c.sourcePath.aux4Bound = true
	c.sourcePath.extraAux4[0].generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.extraAux4[0].pconn.mu.Lock()
	c.sourcePath.extraAux4[0].pconn.setConnLocked(aux2Conn, "udp4", 1)
	c.sourcePath.extraAux4[0].pconn.mu.Unlock()
	c.sourcePath.extra4Bound[0] = true
	c.sourcePath.mu.Unlock()

	apA := udpConnAddrPort(t, dstA.LocalAddr())
	apB := udpConnAddrPort(t, dstB.LocalAddr())
	apStandby := udpConnAddrPort(t, dstStandby.LocalAddr())
	epA := epAddr{ap: apA}
	epB := epAddr{ap: apB}
	epStandby := epAddr{ap: apStandby}
	sources := c.sourcePathProbeSources(true)
	if len(sources) != 2 {
		t.Fatalf("IPv4 probe sources = %+v, want two aux candidates", sources)
	}
	aux1, aux2 := sources[0], sources[1]

	now := mono.Now()
	c.mu.Lock()
	for i := 0; i < sourcePathMinSamplesForUse; i++ {
		at := now.Add(-time.Duration(i) * time.Millisecond)
		c.sourceProbes.samples = append(c.sourceProbes.samples,
			sourcePathProbeSample{dst: epA, source: aux1, latency: 30 * time.Millisecond, at: at},
			sourcePathProbeSample{dst: epA, source: aux2, latency: 10 * time.Millisecond, at: at},
			sourcePathProbeSample{dst: epB, source: aux1, latency: 5 * time.Millisecond, at: at},
			sourcePathProbeSample{dst: epB, source: aux2, latency: 40 * time.Millisecond, at: at},
			sourcePathProbeSample{dst: epStandby, source: aux1, latency: 15 * time.Millisecond, at: at},
			sourcePathProbeSample{dst: epStandby, source: aux2, latency: 20 * time.Millisecond, at: at},
		)
	}
	c.mu.Unlock()

	stateA := &endpointState{}
	stateA.addPongReplyLocked(pongReply{latency: 50 * time.Millisecond, pongAt: now})
	stateB := &endpointState{}
	stateB.addPongReplyLocked(pongReply{latency: 50 * time.Millisecond, pongAt: now})
	stateStandby := &endpointState{}
	stateStandby.addPongReplyLocked(pongReply{latency: 50 * time.Millisecond, pongAt: now})
	de := &endpoint{
		c:                 &c,
		heartbeatDisabled: true,
		endpointState: map[netip.AddrPort]*endpointState{
			apA:       stateA,
			apB:       stateB,
			apStandby: stateStandby,
		},
	}

	payload := sourcePathTestTransportPacket(103, 1)
	buf := make([]byte, packet.GeneveFixedHeaderLength+len(payload))
	copy(buf[packet.GeneveFixedHeaderLength:], payload)
	before := metricSourcePathDualSendPackets.Value()
	if err := c.Send([][]byte{buf}, de, packet.GeneveFixedHeaderLength); err != nil {
		t.Fatalf("Conn.Send returned error: %v", err)
	}
	if got := metricSourcePathDualSendPackets.Value() - before; got != 1 {
		t.Fatalf("dual-send packets metric delta = %d, want 1", got)
	}

	wantA := udpConnAddrPort(t, aux2Conn.LocalAddr())
	wantB := udpConnAddrPort(t, aux1Conn.LocalAddr())
	for name, conn := range map[string]struct {
		conn    *net.UDPConn
		wantSrc netip.AddrPort
	}{
		"dstA": {conn: dstA, wantSrc: wantA},
		"dstB": {conn: dstB, wantSrc: wantB},
	} {
		if err := conn.conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 128)
		n, src, err := conn.conn.ReadFromUDPAddrPort(got)
		if err != nil {
			t.Fatalf("%s ReadFromUDPAddrPort: %v", name, err)
		}
		if string(got[:n]) != string(payload) {
			t.Fatalf("%s received payload %q, want %q", name, got[:n], payload)
		}
		if src.Port() != conn.wantSrc.Port() {
			t.Fatalf("%s source port = %d, want %d", name, src.Port(), conn.wantSrc.Port())
		}
	}
	assertNoUDPConnPayload(t, dstStandby)
}

func TestSourcePathRankedDualSendPathsRequireWarmedSamples(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "2")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
	})

	var c Conn
	c.sourcePath.generation = 1
	c.sourcePath.mu.Lock()
	c.ensureSourcePathAuxSocketCountLocked(2)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true
	c.sourcePath.extraAux4[0].generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.extra4Bound[0] = true
	c.sourcePath.mu.Unlock()

	sources := c.sourcePathProbeSources(true)
	if len(sources) != 2 {
		t.Fatalf("IPv4 probe sources = %+v, want two aux candidates", sources)
	}
	aux1, aux2 := sources[0], sources[1]

	now := mono.Now()
	warmDst := epAddr{ap: netip.MustParseAddrPort("198.51.100.10:41641")}
	coldDst := epAddr{ap: netip.MustParseAddrPort("198.51.100.11:41641")}
	candidates := []sourcePathDstCandidate{
		{dst: warmDst, primaryRTT: 25 * time.Millisecond, hasPrimaryRTT: true},
		{dst: coldDst},
	}

	if ranked := c.sourcePathRankedDualSendPaths(candidates, now); len(ranked) != 0 {
		t.Fatalf("ranked paths without warmed aux samples = %+v, want none", ranked)
	}

	c.mu.Lock()
	for i := 0; i < sourcePathMinSamplesForUse; i++ {
		c.sourceProbes.samples = append(c.sourceProbes.samples, sourcePathProbeSample{
			dst:     warmDst,
			source:  aux1,
			latency: 7 * time.Millisecond,
			at:      now.Add(-time.Duration(i) * time.Millisecond),
		})
	}
	c.mu.Unlock()

	ranked := c.sourcePathRankedDualSendPaths(candidates, now)
	if len(ranked) != 2 {
		t.Fatalf("ranked paths = %+v, want primary + one warmed aux path", ranked)
	}
	seenPrimary := false
	seenAux1 := false
	for _, path := range ranked {
		if path.dst != warmDst {
			t.Fatalf("ranked path dst = %v, want only warmed dst %v", path.dst, warmDst)
		}
		switch path.source {
		case primarySourceRxMeta:
			seenPrimary = true
		case aux1:
			seenAux1 = true
		case aux2:
			t.Fatalf("unprobed aux source entered ranked path pool: %+v", path)
		default:
			t.Fatalf("unexpected ranked path source: %+v", path)
		}
	}
	if !seenPrimary || !seenAux1 {
		t.Fatalf("ranked paths = %+v, want primary and warmed aux1", ranked)
	}
}

func TestSourcePathRankedDualSendPathsCapsWarmedPathPool(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
	})

	var c Conn
	c.sourcePath.generation = 1
	c.sourcePath.mu.Lock()
	c.ensureSourcePathAuxSocketCountLocked(1)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true
	c.sourcePath.mu.Unlock()

	sources := c.sourcePathProbeSources(true)
	if len(sources) != 1 {
		t.Fatalf("IPv4 probe sources = %+v, want one aux candidate", sources)
	}
	aux := sources[0]

	now := mono.Now()
	candidates := make([]sourcePathDstCandidate, sourcePathMaxRankedPaths+8)
	c.mu.Lock()
	for i := range candidates {
		dst := epAddr{ap: netip.AddrPortFrom(netip.MustParseAddr("198.51.100.20"), uint16(40000+i))}
		candidates[i] = sourcePathDstCandidate{
			dst:           dst,
			primaryRTT:    80 * time.Millisecond,
			hasPrimaryRTT: true,
		}
		for sample := 0; sample < sourcePathMinSamplesForUse; sample++ {
			c.sourceProbes.samples = append(c.sourceProbes.samples, sourcePathProbeSample{
				dst:     dst,
				source:  aux,
				latency: time.Duration(i+1) * time.Millisecond,
				at:      now.Add(-time.Duration(sample) * time.Millisecond),
			})
		}
	}
	c.mu.Unlock()

	ranked := c.sourcePathRankedDualSendPaths(candidates, now)
	if len(ranked) != sourcePathMaxRankedPaths {
		t.Fatalf("ranked path count = %d, want capped count %d", len(ranked), sourcePathMaxRankedPaths)
	}
	for _, path := range ranked {
		if path.source != aux {
			t.Fatalf("ranked path source = %+v, want fastest warmed aux paths only before slow primaries", path.source)
		}
		if !path.hasLatency {
			t.Fatalf("ranked path has no measured latency: %+v", path)
		}
	}
}

func TestSourcePathActivePolicyHourlyRefreshAndPromotionCooldown(t *testing.T) {
	now := mono.Now()
	dstA := epAddr{ap: netip.MustParseAddrPort("192.0.2.10:41641")}
	dstB := epAddr{ap: netip.MustParseAddrPort("192.0.2.11:41641")}
	auxA := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 1}
	auxB := sourceRxMeta{socketID: sourceIPv4ExtraSocketIDBase, generation: 1}
	standbyA := sourceRxMeta{socketID: sourceIPv4ExtraSocketIDBase + 1, generation: 1}
	standbyB := sourceRxMeta{socketID: sourceIPv4ExtraSocketIDBase + 2, generation: 1}
	path := func(dst epAddr, source sourceRxMeta, latency time.Duration) sourcePathSendPath {
		return sourcePathSendPath{
			dst:        dst,
			source:     source,
			latency:    latency,
			hasLatency: true,
			lastAt:     now,
		}
	}

	activeA := path(dstA, auxA, 10*time.Millisecond)
	activeB := path(dstB, auxB, 20*time.Millisecond)
	fastStandbyA := path(dstA, standbyA, 5*time.Millisecond)
	fastStandbyB := path(dstB, standbyB, 6*time.Millisecond)

	de := &endpoint{}
	de.mu.Lock()
	selected, ev := de.sourcePathApplyActivePathPolicyLocked(now, []sourcePathSendPath{
		activeA,
		activeB,
		path(dstA, standbyA, 30*time.Millisecond),
		path(dstB, standbyB, 40*time.Millisecond),
	})
	if ev.replaced {
		de.mu.Unlock()
		t.Fatalf("initial active selection replaced path unexpectedly")
	}
	if !sameSourcePathSendPath(selected[0], activeA) || !sameSourcePathSendPath(selected[1], activeB) {
		de.mu.Unlock()
		t.Fatalf("initial active paths = %+v, want activeA/activeB", selected)
	}

	selected, ev = de.sourcePathApplyActivePathPolicyLocked(now.Add(30*time.Minute), []sourcePathSendPath{
		fastStandbyA,
		fastStandbyB,
		activeA,
		activeB,
	})
	if ev.replaced {
		de.mu.Unlock()
		t.Fatalf("standby promoted before hourly refresh: %+v", ev)
	}
	if !sameSourcePathSendPath(selected[0], activeA) || !sameSourcePathSendPath(selected[1], activeB) {
		de.mu.Unlock()
		t.Fatalf("active paths changed before hourly refresh = %+v", selected)
	}

	refreshAt := now.Add(sourcePathStandbyRefreshEvery + time.Second)
	selected, ev = de.sourcePathApplyActivePathPolicyLocked(refreshAt, []sourcePathSendPath{
		fastStandbyA,
		fastStandbyB,
		activeA,
		activeB,
	})
	if !ev.refreshed || !ev.replaced {
		de.mu.Unlock()
		t.Fatalf("hourly refresh event = %+v, want refresh plus one promotion", ev)
	}
	if !sameSourcePathSendPath(selected[0], fastStandbyA) || !sameSourcePathSendPath(selected[1], activeB) {
		de.mu.Unlock()
		t.Fatalf("after first promotion active paths = %+v, want fastStandbyA/activeB", selected)
	}

	selected, ev = de.sourcePathApplyActivePathPolicyLocked(refreshAt.Add(30*time.Second), []sourcePathSendPath{
		fastStandbyA,
		fastStandbyB,
		activeA,
		activeB,
	})
	if ev.replaced {
		de.mu.Unlock()
		t.Fatalf("second path promoted before cooldown elapsed: %+v", ev)
	}
	if !sameSourcePathSendPath(selected[0], fastStandbyA) || !sameSourcePathSendPath(selected[1], activeB) {
		de.mu.Unlock()
		t.Fatalf("active paths changed during cooldown = %+v", selected)
	}

	selected, ev = de.sourcePathApplyActivePathPolicyLocked(refreshAt.Add(sourcePathActiveReplaceCooldown+time.Second), []sourcePathSendPath{
		fastStandbyA,
		fastStandbyB,
		activeA,
		activeB,
	})
	de.mu.Unlock()
	if !ev.replaced {
		t.Fatalf("second path did not promote after cooldown: %+v", ev)
	}
	if !sameSourcePathSendPath(selected[0], fastStandbyA) || !sameSourcePathSendPath(selected[1], fastStandbyB) {
		t.Fatalf("after second promotion active paths = %+v, want fastStandbyA/fastStandbyB", selected)
	}
}

func TestSourcePathActivePolicyInitialSelectionUsesTwoFastestPaths(t *testing.T) {
	now := mono.Now()
	dstA := epAddr{ap: netip.MustParseAddrPort("192.0.2.10:41641")}
	dstB := epAddr{ap: netip.MustParseAddrPort("192.0.2.11:41641")}
	path := func(dst epAddr, source sourceRxMeta, latency time.Duration) sourcePathSendPath {
		return sourcePathSendPath{
			dst:        dst,
			source:     source,
			latency:    latency,
			hasLatency: true,
			lastAt:     now,
		}
	}

	fastA := path(dstA, sourceRxMeta{socketID: sourceIPv4SocketID, generation: 1}, 5*time.Millisecond)
	secondA := path(dstA, sourceRxMeta{socketID: sourceIPv4ExtraSocketIDBase, generation: 1}, 6*time.Millisecond)
	slowerB := path(dstB, sourceRxMeta{socketID: sourceIPv4ExtraSocketIDBase + 1, generation: 1}, 25*time.Millisecond)

	de := &endpoint{}
	de.mu.Lock()
	selected, ev := de.sourcePathApplyActivePathPolicyLocked(now, []sourcePathSendPath{
		fastA,
		secondA,
		slowerB,
	})
	de.mu.Unlock()

	if ev.replaced {
		t.Fatalf("initial active selection replaced path unexpectedly")
	}
	if len(selected) != 2 || !sameSourcePathSendPath(selected[0], fastA) || !sameSourcePathSendPath(selected[1], secondA) {
		t.Fatalf("initial active paths = %+v, want two fastest paths sharing dstA", selected)
	}
}

func TestSourcePathActivePolicyRefillPreservesEndpointDiversity(t *testing.T) {
	now := mono.Now()
	dstA := epAddr{ap: netip.MustParseAddrPort("192.0.2.10:41641")}
	dstB := epAddr{ap: netip.MustParseAddrPort("192.0.2.11:41641")}
	path := func(dst epAddr, source sourceRxMeta, latency time.Duration) sourcePathSendPath {
		return sourcePathSendPath{
			dst:        dst,
			source:     source,
			latency:    latency,
			hasLatency: true,
			lastAt:     now,
		}
	}

	activeA := path(dstA, sourceRxMeta{socketID: sourceIPv4SocketID, generation: 1}, 5*time.Millisecond)
	missingB := path(dstB, sourceRxMeta{socketID: sourceIPv4ExtraSocketIDBase, generation: 1}, 7*time.Millisecond)
	fasterSameDstA := path(dstA, sourceRxMeta{socketID: sourceIPv4ExtraSocketIDBase + 1, generation: 1}, 6*time.Millisecond)
	refillB := path(dstB, sourceRxMeta{socketID: sourceIPv4ExtraSocketIDBase + 2, generation: 1}, 25*time.Millisecond)

	de := &endpoint{
		sourcePathActivePaths: [2]sourcePathSendPath{activeA, missingB},
		sourcePathActiveCount: 2,
	}
	de.mu.Lock()
	selected, ev := de.sourcePathApplyActivePathPolicyLocked(now, []sourcePathSendPath{
		activeA,
		fasterSameDstA,
		refillB,
	})
	de.mu.Unlock()

	if ev.replaced {
		t.Fatalf("active refill replaced path unexpectedly")
	}
	if len(selected) != 2 || !sameSourcePathSendPath(selected[0], activeA) || !sameSourcePathSendPath(selected[1], refillB) {
		t.Fatalf("refilled active paths = %+v, want activeA plus distinct dstB refill", selected)
	}
}

func TestSourcePathDualSendObservedRemoteEndpointsSkipStaleAlternate(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
	})

	now := mono.Now()
	primary := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}
	fresh := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:51641")}
	stale := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:61641")}
	de := &endpoint{
		endpointState: map[netip.AddrPort]*endpointState{
			fresh.ap: &endpointState{},
			stale.ap: &endpointState{},
		},
		sourcePathRemoteSlots: [2]epAddr{primary, stale},
		sourcePathRemoteSeen:  [2]mono.Time{now, now.Add(-2 * sessionActiveTimeout)},
	}
	de.mu.Lock()
	if got := de.dualSendObservedEndpointAddrsForSendLocked(primary, now); got != nil {
		de.mu.Unlock()
		t.Fatalf("observed dual endpoints with stale alternate = %+v, want nil", got)
	}
	de.sourcePathRemoteSlots[1] = fresh
	de.sourcePathRemoteSeen[1] = now
	got := de.dualSendObservedEndpointAddrsForSendLocked(primary, now)
	de.mu.Unlock()
	if len(got) != 2 || got[0] != primary || got[1] != fresh {
		t.Fatalf("observed dual endpoints = %+v, want primary/fresh", got)
	}
}

func TestSourcePathDualEndpointStrategyInvalidatesBadEndpoints(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "dual-endpoint")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
	})

	pl := &recordingPacketListener{
		base:     localhostListener{},
		failures: map[netip.AddrPort]error{},
	}
	pc, err := pl.ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	t.Cleanup(func() { pc.Close() })
	local, ok := addrPortFromNetAddr(pc.LocalAddr())
	if !ok {
		t.Fatalf("could not parse local address %v", pc.LocalAddr())
	}
	pl.setFailure(local, &net.OpError{Op: "write", Err: syscall.EHOSTUNREACH})
	t.Cleanup(pl.clearFailures)

	var c Conn
	c.metrics = new(metrics)
	c.logf = func(string, ...any) {}
	c.pconn4.mu.Lock()
	c.pconn4.setConnLocked(pc.(nettype.PacketConn), "udp4", 1)
	c.pconn4.mu.Unlock()

	now := mono.Now()
	fast := netip.MustParseAddrPort("127.0.0.1:11001")
	mid := netip.MustParseAddrPort("127.0.0.1:11002")
	slow := netip.MustParseAddrPort("127.0.0.1:11003")
	fastState := &endpointState{}
	fastState.addPongReplyLocked(pongReply{latency: 10 * time.Millisecond, pongAt: now})
	midState := &endpointState{}
	midState.addPongReplyLocked(pongReply{latency: 20 * time.Millisecond, pongAt: now})
	slowState := &endpointState{}
	slowState.addPongReplyLocked(pongReply{latency: 50 * time.Millisecond, pongAt: now})

	de := &endpoint{
		c:                 &c,
		heartbeatDisabled: true,
		endpointState: map[netip.AddrPort]*endpointState{
			slow: slowState,
			mid:  midState,
			fast: fastState,
		},
	}

	payload := sourcePathTestTransportPacket(105, 1)
	buf := make([]byte, packet.GeneveFixedHeaderLength+len(payload))
	copy(buf[packet.GeneveFixedHeaderLength:], payload)
	if err := de.send([][]byte{buf}, packet.GeneveFixedHeaderLength); !isBadEndpointErr(err) {
		t.Fatalf("endpoint send error = %v, want bad endpoint error", err)
	}

	de.mu.Lock()
	defer de.mu.Unlock()
	if _, ok := fastState.latencyLocked(); ok {
		t.Fatal("primary dual endpoint still has latency after bad endpoint error")
	}
	if _, ok := midState.latencyLocked(); ok {
		t.Fatal("secondary dual endpoint still has latency after bad endpoint error")
	}
	if _, ok := slowState.latencyLocked(); !ok {
		t.Fatal("unselected endpoint lost latency after selected endpoints failed")
	}
}

func TestSourcePathDataSendSourceForcedAuxDualStack(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	})

	var c Conn
	c.sourcePath.generation = 7
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true
	c.sourcePath.aux6.setID(sourceIPv6SocketID)
	c.sourcePath.aux6.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux6Bound = true

	v4 := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}
	v6 := epAddr{ap: netip.MustParseAddrPort("[2001:db8::1]:41641")}

	if !sourcePathEnabled() {
		t.Fatalf("TS_EXPERIMENTAL_SRCSEL_ENABLE was not enabled")
	}
	if got := envknobSrcSelAuxSockets(); got != 1 {
		t.Fatalf("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS = %d, want 1", got)
	}
	if got := sourcePathAuxSocketCount(); got != 1 {
		t.Fatalf("sourcePathAuxSocketCount() = %d, want 1", got)
	}
	if !sourcePathForcedDataSourceAllowsAddr(v4.ap.Addr()) {
		t.Fatalf("forced source data policy rejected IPv4 address %v", v4.ap.Addr())
	}
	if !sourcePathForcedDataSourceAllowsAddr(v6.ap.Addr()) {
		t.Fatalf("forced source data policy rejected IPv6 address %v", v6.ap.Addr())
	}

	if got := c.sourcePathDataSendSource(v4); got.socketID != sourceIPv4SocketID {
		t.Fatalf("forced IPv4 source socket = %d, want %d", got.socketID, sourceIPv4SocketID)
	}
	if got := c.sourcePathDataSendSource(v6); got.socketID != sourceIPv6SocketID {
		t.Fatalf("forced IPv6 source socket = %d, want %d", got.socketID, sourceIPv6SocketID)
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux4")
	if got := c.sourcePathDataSendSource(v4); got.socketID != sourceIPv4SocketID {
		t.Fatalf("aux4 forced IPv4 source socket = %d, want %d", got.socketID, sourceIPv4SocketID)
	}
	if got := c.sourcePathDataSendSource(v6); !got.isPrimary() {
		t.Fatalf("aux4 forced IPv6 source socket = %d, want primary", got.socketID)
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux6")
	if got := c.sourcePathDataSendSource(v4); !got.isPrimary() {
		t.Fatalf("aux6 forced IPv4 source socket = %d, want primary", got.socketID)
	}
	if got := c.sourcePathDataSendSource(v6); got.socketID != sourceIPv6SocketID {
		t.Fatalf("aux6 forced IPv6 source socket = %d, want %d", got.socketID, sourceIPv6SocketID)
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
	if got := c.sourcePathDataSendSource(v4); !got.isPrimary() {
		t.Fatalf("unforced IPv4 source socket = %d, want primary", got.socketID)
	}
	if got := c.sourcePathDataSendSource(v6); !got.isPrimary() {
		t.Fatalf("unforced IPv6 source socket = %d, want primary", got.socketID)
	}
}

func TestSourcePathBestCandidateObserveOnlyDoesNotSelectDataSource(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	})

	var c Conn
	c.sourcePath.generation = 17
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true
	c.sourcePath.aux6.setID(sourceIPv6SocketID)
	c.sourcePath.aux6.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux6Bound = true

	v4 := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}
	v6 := epAddr{ap: netip.MustParseAddrPort("[2001:db8::1]:41641")}
	sources4 := c.sourcePathProbeSources(true)
	sources6 := c.sourcePathProbeSources(false)
	if len(sources4) != 1 || sources4[0].socketID != sourceIPv4SocketID {
		t.Fatalf("IPv4 probe sources = %+v, want one IPv4 auxiliary source", sources4)
	}
	if len(sources6) != 1 || sources6[0].socketID != sourceIPv6SocketID {
		t.Fatalf("IPv6 probe sources = %+v, want one IPv6 auxiliary source", sources6)
	}

	sentinel := time.Unix(321, 0)
	c.lastErrRebind.Store(sentinel)
	now := mono.Now()

	c.mu.Lock()
	c.sourceProbes.pending = map[stun.TxID]sourcePathProbeTx{
		stun.NewTxID(): {
			dst:    v4,
			source: sources4[0],
			at:     now,
		},
	}
	c.sourceProbes.samples = []sourcePathProbeSample{
		{dst: v4, source: sources4[0], latency: 8 * time.Millisecond, at: now.Add(-3 * time.Second)},
		{dst: v4, source: sources4[0], latency: 9 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: v4, source: sources4[0], latency: 7 * time.Millisecond, at: now.Add(-1 * time.Second)},
		{dst: v6, source: sources6[0], latency: 13 * time.Millisecond, at: now.Add(-3 * time.Second)},
		{dst: v6, source: sources6[0], latency: 9 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: v6, source: sources6[0], latency: 11 * time.Millisecond, at: now.Add(-1 * time.Second)},
	}
	beforePending, beforeSamples := c.sourceProbes.pendingLenLocked(), c.sourceProbes.samplesLenLocked()
	c.mu.Unlock()

	score4, ok4 := c.sourcePathBestCandidate(v4)
	score6, ok6 := c.sourcePathBestCandidate(v6)

	c.mu.Lock()
	afterPending, afterSamples := c.sourceProbes.pendingLenLocked(), c.sourceProbes.samplesLenLocked()
	c.mu.Unlock()

	if !ok4 {
		t.Fatal("IPv4 observe-only candidate not found")
	}
	if score4.source != sources4[0] {
		t.Fatalf("IPv4 observe-only candidate source = %+v, want %+v", score4.source, sources4[0])
	}
	if score4.latency != 8*time.Millisecond || score4.samples != 3 {
		t.Fatalf("IPv4 observe-only score = latency %v samples %d, want 8ms and 3 samples (mean of 8,9,7)", score4.latency, score4.samples)
	}
	if !ok6 {
		t.Fatal("IPv6 observe-only candidate not found")
	}
	if score6.source != sources6[0] {
		t.Fatalf("IPv6 observe-only candidate source = %+v, want %+v", score6.source, sources6[0])
	}
	if score6.latency != 11*time.Millisecond || score6.samples != 3 {
		t.Fatalf("IPv6 observe-only score = latency %v samples %d, want 11ms and 3 samples (mean of 13,9,11)", score6.latency, score6.samples)
	}
	if afterPending != beforePending {
		t.Fatalf("pending probes mutated by observe-only scoring: got %d want %d", afterPending, beforePending)
	}
	if afterSamples != beforeSamples {
		t.Fatalf("samples mutated by observe-only scoring: got %d want %d", afterSamples, beforeSamples)
	}
	if got := c.sourcePathDataSendSource(v4); !got.isPrimary() {
		t.Fatalf("unforced IPv4 data source = %+v, want primary", got)
	}
	if got := c.sourcePathDataSendSource(v6); !got.isPrimary() {
		t.Fatalf("unforced IPv6 data source = %+v, want primary", got)
	}
	if got := c.lastErrRebind.Load(); !got.Equal(sentinel) {
		t.Fatalf("observe-only scoring updated lastErrRebind: got %v want %v", got, sentinel)
	}
}

func TestSourcePathDataSendSourceAutomaticCandidateDualStack(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	})

	var c Conn
	c.sourcePath.generation = 19
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true
	c.sourcePath.aux6.setID(sourceIPv6SocketID)
	c.sourcePath.aux6.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux6Bound = true

	v4 := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}
	v4Other := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	v4NoSample := epAddr{ap: netip.MustParseAddrPort("192.0.2.3:41641")}
	v6 := epAddr{ap: netip.MustParseAddrPort("[2001:db8::1]:41641")}
	sources4 := c.sourcePathProbeSources(true)
	sources6 := c.sourcePathProbeSources(false)
	if len(sources4) != 1 || sources4[0].socketID != sourceIPv4SocketID {
		t.Fatalf("IPv4 probe sources = %+v, want one IPv4 auxiliary source", sources4)
	}
	if len(sources6) != 1 || sources6[0].socketID != sourceIPv6SocketID {
		t.Fatalf("IPv6 probe sources = %+v, want one IPv6 auxiliary source", sources6)
	}

	sentinel := time.Unix(654, 0)
	c.lastErrRebind.Store(sentinel)
	now := mono.Now()
	stale4 := sourceRxMeta{socketID: sourceIPv4SocketID, generation: sourceGeneration(c.sourcePath.generation - 1)}

	c.mu.Lock()
	c.sourceProbes.pending = map[stun.TxID]sourcePathProbeTx{
		stun.NewTxID(): {
			dst:    v4,
			source: sources4[0],
			at:     now,
		},
	}
	c.sourceProbes.samples = []sourcePathProbeSample{
		{dst: v4, source: primarySourceRxMeta, latency: time.Millisecond, at: now.Add(-5 * time.Second)},
		{dst: v4, source: stale4, latency: time.Millisecond, at: now.Add(-4 * time.Second)},
		{dst: v4Other, source: sources4[0], latency: time.Millisecond, at: now.Add(-3 * time.Second)},
		{dst: v4, source: sources4[0], latency: 7 * time.Millisecond, at: now.Add(-3 * time.Second)},
		{dst: v4, source: sources4[0], latency: 8 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: v4, source: sources4[0], latency: 6 * time.Millisecond, at: now.Add(-1 * time.Second)},
		{dst: v6, source: sources6[0], latency: 11 * time.Millisecond, at: now.Add(-3 * time.Second)},
		{dst: v6, source: sources6[0], latency: 9 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: v6, source: sources6[0], latency: 10 * time.Millisecond, at: now.Add(-1 * time.Second)},
	}
	beforePending, beforeSamples := c.sourceProbes.pendingLenLocked(), c.sourceProbes.samplesLenLocked()
	c.mu.Unlock()

	if got := c.sourcePathDataSendSource(v4); got != sources4[0] {
		t.Fatalf("automatic IPv4 data source = %+v, want %+v", got, sources4[0])
	}
	if got := c.sourcePathDataSendSource(v6); got != sources6[0] {
		t.Fatalf("automatic IPv6 data source = %+v, want %+v", got, sources6[0])
	}
	if got := c.sourcePathDataSendSource(v4NoSample); !got.isPrimary() {
		t.Fatalf("automatic no-sample IPv4 data source = %+v, want primary", got)
	}

	c.mu.Lock()
	afterPending, afterSamples := c.sourceProbes.pendingLenLocked(), c.sourceProbes.samplesLenLocked()
	c.mu.Unlock()
	if afterPending != beforePending {
		t.Fatalf("pending probes mutated by automatic source selection: got %d want %d", afterPending, beforePending)
	}
	if afterSamples != beforeSamples {
		t.Fatalf("samples mutated by automatic source selection: got %d want %d", afterSamples, beforeSamples)
	}
	if got := c.lastErrRebind.Load(); !got.Equal(sentinel) {
		t.Fatalf("automatic source selection updated lastErrRebind: got %v want %v", got, sentinel)
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux4")
	if got := c.sourcePathDataSendSource(v6); !got.isPrimary() {
		t.Fatalf("aux4 forced IPv6 source with auto enabled = %+v, want primary", got)
	}
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux6")
	if got := c.sourcePathDataSendSource(v4); !got.isPrimary() {
		t.Fatalf("aux6 forced IPv4 source with auto enabled = %+v, want primary", got)
	}
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	if got := c.sourcePathDataSendSource(v4); !got.isPrimary() {
		t.Fatalf("auto-disabled IPv4 data source = %+v, want primary", got)
	}
}

func TestSourcePathDataSendSourceNonDirectGuardDualStack(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	})

	var c Conn
	c.sourcePath.generation = 23
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true
	c.sourcePath.aux6.setID(sourceIPv6SocketID)
	c.sourcePath.aux6.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux6Bound = true

	source4 := c.sourcePath.aux4.rxMeta()
	source6 := c.sourcePath.aux6.rxMeta()
	direct4 := epAddr{ap: netip.MustParseAddrPort("192.0.2.10:41641")}
	direct6 := epAddr{ap: netip.MustParseAddrPort("[2001:db8::10]:41641")}
	var relayVNI packet.VirtualNetworkID
	relayVNI.Set(1)
	relay4 := epAddr{ap: direct4.ap, vni: relayVNI}
	relay6 := epAddr{ap: direct6.ap, vni: relayVNI}

	if got := c.sourcePathDataSendSource(direct4); got != source4 {
		t.Fatalf("forced direct IPv4 source = %+v, want %+v", got, source4)
	}
	if got := c.sourcePathDataSendSource(direct6); got != source6 {
		t.Fatalf("forced direct IPv6 source = %+v, want %+v", got, source6)
	}
	if got := c.sourcePathDataSendSource(relay4); !got.isPrimary() {
		t.Fatalf("forced non-direct IPv4 source = %+v, want primary", got)
	}
	if got := c.sourcePathDataSendSource(relay6); !got.isPrimary() {
		t.Fatalf("forced non-direct IPv6 source = %+v, want primary", got)
	}

	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
	now := mono.Now()
	c.mu.Lock()
	c.sourceProbes.pending = map[stun.TxID]sourcePathProbeTx{
		stun.NewTxID(): {dst: direct4, source: source4, at: now},
		stun.NewTxID(): {dst: direct6, source: source6, at: now},
	}
	c.sourceProbes.samples = []sourcePathProbeSample{
		{dst: direct4, source: source4, latency: 6 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: direct4, source: source4, latency: 6 * time.Millisecond, at: now.Add(-1 * time.Second)},
		{dst: direct4, source: source4, latency: 6 * time.Millisecond, at: now},
		{dst: direct6, source: source6, latency: 7 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: direct6, source: source6, latency: 7 * time.Millisecond, at: now.Add(-1 * time.Second)},
		{dst: direct6, source: source6, latency: 7 * time.Millisecond, at: now},
		{dst: relay4, source: source4, latency: time.Millisecond, at: now},
		{dst: relay6, source: source6, latency: time.Millisecond, at: now},
	}
	beforePending, beforeSamples := c.sourceProbes.pendingLenLocked(), c.sourceProbes.samplesLenLocked()
	c.mu.Unlock()

	if got := c.sourcePathDataSendSource(direct4); got != source4 {
		t.Fatalf("automatic direct IPv4 source = %+v, want %+v", got, source4)
	}
	if got := c.sourcePathDataSendSource(direct6); got != source6 {
		t.Fatalf("automatic direct IPv6 source = %+v, want %+v", got, source6)
	}
	if got := c.sourcePathDataSendSource(relay4); !got.isPrimary() {
		t.Fatalf("automatic non-direct IPv4 source = %+v, want primary", got)
	}
	if got := c.sourcePathDataSendSource(relay6); !got.isPrimary() {
		t.Fatalf("automatic non-direct IPv6 source = %+v, want primary", got)
	}

	c.mu.Lock()
	afterPending, afterSamples := c.sourceProbes.pendingLenLocked(), c.sourceProbes.samplesLenLocked()
	c.mu.Unlock()
	if afterPending != beforePending {
		t.Fatalf("non-direct guard mutated pending probes: got %d want %d", afterPending, beforePending)
	}
	if afterSamples != beforeSamples {
		t.Fatalf("non-direct guard mutated samples: got %d want %d", afterSamples, beforeSamples)
	}
}

func TestSourcePathRebindDisabledClosesAuxAndClearsState(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "false")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	})

	var c Conn
	c.sourcePath.generation = 29
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux6.setID(sourceIPv6SocketID)
	c.sourcePath.aux6.generation.Store(uint64(c.sourcePath.generation))

	auxErr := errors.New("unused source path packet conn")
	aux4 := &failingSourcePathPacketConn{
		local: net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:12345")),
		err:   auxErr,
	}
	aux6 := &failingSourcePathPacketConn{
		local: net.UDPAddrFromAddrPort(netip.MustParseAddrPort("[::1]:12345")),
		err:   auxErr,
	}

	c.sourcePath.aux4.pconn.mu.Lock()
	c.sourcePath.aux4.pconn.setConnLocked(aux4, "udp4", 1)
	c.sourcePath.aux4.pconn.mu.Unlock()
	c.sourcePath.aux6.pconn.mu.Lock()
	c.sourcePath.aux6.pconn.setConnLocked(aux6, "udp6", 1)
	c.sourcePath.aux6.pconn.mu.Unlock()
	c.sourcePath.aux4Bound = true
	c.sourcePath.aux6Bound = true

	v4 := epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}
	v6 := epAddr{ap: netip.MustParseAddrPort("[2001:db8::1]:41641")}
	source4 := c.sourcePath.aux4.rxMeta()
	source6 := c.sourcePath.aux6.rxMeta()
	now := mono.Now()

	c.mu.Lock()
	c.sourceProbes.pending = map[stun.TxID]sourcePathProbeTx{
		stun.NewTxID(): {dst: v4, source: source4, at: now},
		stun.NewTxID(): {dst: v6, source: source6, at: now},
	}
	c.sourceProbes.samples = []sourcePathProbeSample{
		{dst: v4, source: source4, latency: time.Millisecond, at: now},
		{dst: v6, source: source6, latency: time.Millisecond, at: now},
	}
	c.mu.Unlock()

	if err := c.rebindSourcePathSockets(); err != nil {
		t.Fatal(err)
	}

	if !aux4.closed {
		t.Fatal("disabled rebind did not close IPv4 auxiliary socket")
	}
	if !aux6.closed {
		t.Fatal("disabled rebind did not close IPv6 auxiliary socket")
	}

	c.sourcePath.mu.Lock()
	aux4Bound, aux6Bound := c.sourcePath.aux4Bound, c.sourcePath.aux6Bound
	c.sourcePath.mu.Unlock()
	if aux4Bound || aux6Bound {
		t.Fatalf("disabled rebind left aux bound state set: aux4=%v aux6=%v", aux4Bound, aux6Bound)
	}

	c.mu.Lock()
	pending, samples := c.sourceProbes.pendingLenLocked(), c.sourceProbes.samplesLenLocked()
	c.mu.Unlock()
	if pending != 0 || samples != 0 {
		t.Fatalf("disabled rebind left source probe state: pending=%d samples=%d", pending, samples)
	}
	if got := c.sourcePathDataSendSource(v4); !got.isPrimary() {
		t.Fatalf("disabled forced IPv4 source = %+v, want primary", got)
	}
	if got := c.sourcePathDataSendSource(v6); !got.isPrimary() {
		t.Fatalf("disabled forced IPv6 source = %+v, want primary", got)
	}
}

func TestSendUDPBatchFromSourceAuxDualStackLoopback(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	})

	tests := []struct {
		name        string
		network     string
		addr        string
		socketID    SourceSocketID
		bindAuxConn func(*Conn, nettype.PacketConn)
	}{
		{
			name:     "ipv4",
			network:  "udp4",
			addr:     "127.0.0.1:0",
			socketID: sourceIPv4SocketID,
			bindAuxConn: func(c *Conn, pc nettype.PacketConn) {
				c.sourcePath.aux4.setID(sourceIPv4SocketID)
				c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
				c.sourcePath.aux4.pconn.mu.Lock()
				c.sourcePath.aux4.pconn.setConnLocked(pc, "udp4", 1)
				c.sourcePath.aux4.pconn.mu.Unlock()
				c.sourcePath.aux4Bound = true
			},
		},
		{
			name:     "ipv6",
			network:  "udp6",
			addr:     "[::1]:0",
			socketID: sourceIPv6SocketID,
			bindAuxConn: func(c *Conn, pc nettype.PacketConn) {
				c.sourcePath.aux6.setID(sourceIPv6SocketID)
				c.sourcePath.aux6.generation.Store(uint64(c.sourcePath.generation))
				c.sourcePath.aux6.pconn.mu.Lock()
				c.sourcePath.aux6.pconn.setConnLocked(pc, "udp6", 1)
				c.sourcePath.aux6.pconn.mu.Unlock()
				c.sourcePath.aux6Bound = true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dstConn := listenUDPForSourcePathTest(t, tt.network, tt.addr)
			auxConn := listenUDPForSourcePathTest(t, tt.network, tt.addr)

			var c Conn
			c.sourcePath.generation = 11
			tt.bindAuxConn(&c, auxConn)

			dst := epAddr{ap: udpConnAddrPort(t, dstConn.LocalAddr())}
			source := c.sourcePathDataSendSource(dst)
			if source.socketID != tt.socketID {
				t.Fatalf("selected source socket = %d, want %d", source.socketID, tt.socketID)
			}

			payload := []byte("source-path-" + tt.name)
			buf := make([]byte, packet.GeneveFixedHeaderLength+len(payload))
			copy(buf[packet.GeneveFixedHeaderLength:], payload)
			sent, err := c.sendUDPBatchFromSource(source, dst, [][]byte{buf}, packet.GeneveFixedHeaderLength)
			if err != nil {
				t.Fatalf("sendUDPBatchFromSource returned error: %v", err)
			}
			if !sent {
				t.Fatal("sendUDPBatchFromSource reported unsent packet")
			}

			if err := dstConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, 128)
			n, src, err := dstConn.ReadFromUDPAddrPort(got)
			if err != nil {
				t.Fatalf("ReadFromUDPAddrPort: %v", err)
			}
			if string(got[:n]) != string(payload) {
				t.Fatalf("received payload %q, want %q", got[:n], payload)
			}
			if want := udpConnAddrPort(t, auxConn.LocalAddr()); src.Port() != want.Port() {
				t.Fatalf("received source port = %d, want auxiliary port %d", src.Port(), want.Port())
			}
		})
	}
}

func TestLazyEndpointSendIgnoresForcedAuxDataSourceDualStack(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	})

	tests := []struct {
		name            string
		network         string
		addr            string
		socketID        SourceSocketID
		bindPrimaryConn func(*Conn, nettype.PacketConn)
		bindAuxConn     func(*Conn, nettype.PacketConn)
	}{
		{
			name:     "ipv4",
			network:  "udp4",
			addr:     "127.0.0.1:0",
			socketID: sourceIPv4SocketID,
			bindPrimaryConn: func(c *Conn, pc nettype.PacketConn) {
				c.pconn4.mu.Lock()
				c.pconn4.setConnLocked(pc, "udp4", 1)
				c.pconn4.mu.Unlock()
			},
			bindAuxConn: func(c *Conn, pc nettype.PacketConn) {
				c.sourcePath.aux4.setID(sourceIPv4SocketID)
				c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
				c.sourcePath.aux4.pconn.mu.Lock()
				c.sourcePath.aux4.pconn.setConnLocked(pc, "udp4", 1)
				c.sourcePath.aux4.pconn.mu.Unlock()
				c.sourcePath.aux4Bound = true
			},
		},
		{
			name:     "ipv6",
			network:  "udp6",
			addr:     "[::1]:0",
			socketID: sourceIPv6SocketID,
			bindPrimaryConn: func(c *Conn, pc nettype.PacketConn) {
				c.pconn6.mu.Lock()
				c.pconn6.setConnLocked(pc, "udp6", 1)
				c.pconn6.mu.Unlock()
			},
			bindAuxConn: func(c *Conn, pc nettype.PacketConn) {
				c.sourcePath.aux6.setID(sourceIPv6SocketID)
				c.sourcePath.aux6.generation.Store(uint64(c.sourcePath.generation))
				c.sourcePath.aux6.pconn.mu.Lock()
				c.sourcePath.aux6.pconn.setConnLocked(pc, "udp6", 1)
				c.sourcePath.aux6.pconn.mu.Unlock()
				c.sourcePath.aux6Bound = true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dstConn := listenUDPForSourcePathTest(t, tt.network, tt.addr)
			primaryConn := listenUDPForSourcePathTest(t, tt.network, tt.addr)
			auxConn := listenUDPForSourcePathTest(t, tt.network, tt.addr)

			var c Conn
			c.metrics = new(metrics)
			c.logf = func(string, ...any) {}
			c.sourcePath.generation = 17
			tt.bindPrimaryConn(&c, primaryConn)
			tt.bindAuxConn(&c, auxConn)

			dst := epAddr{ap: udpConnAddrPort(t, dstConn.LocalAddr())}
			source := c.sourcePathDataSendSource(dst)
			if source.socketID != tt.socketID {
				t.Fatalf("forced source socket = %d, want auxiliary socket %d", source.socketID, tt.socketID)
			}

			payload := sourcePathTestTransportPacket(104, 1)
			buf := make([]byte, packet.GeneveFixedHeaderLength+len(payload))
			copy(buf[packet.GeneveFixedHeaderLength:], payload)
			if err := c.Send([][]byte{buf}, &lazyEndpoint{src: dst}, packet.GeneveFixedHeaderLength); err != nil {
				t.Fatalf("Conn.Send returned error: %v", err)
			}

			if err := dstConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, 128)
			n, src, err := dstConn.ReadFromUDPAddrPort(got)
			if err != nil {
				t.Fatalf("ReadFromUDPAddrPort: %v", err)
			}
			if string(got[:n]) != string(payload) {
				t.Fatalf("received payload %q, want %q", got[:n], payload)
			}
			if want := udpConnAddrPort(t, primaryConn.LocalAddr()); src.Port() != want.Port() {
				t.Fatalf("received source port = %d, want primary port %d", src.Port(), want.Port())
			}
			if aux := udpConnAddrPort(t, auxConn.LocalAddr()); src.Port() == aux.Port() {
				t.Fatalf("received source port = auxiliary port %d, want primary path", aux.Port())
			}
		})
	}
}

func TestSourcePathWriteWireGuardBatchToRejectsStaleAuxSource(t *testing.T) {
	dstConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	auxConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")

	var c Conn
	c.sourcePath.generation = 11
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4.pconn.mu.Lock()
	c.sourcePath.aux4.pconn.setConnLocked(auxConn, "udp4", 1)
	c.sourcePath.aux4.pconn.mu.Unlock()
	c.sourcePath.aux4Bound = true

	payload := []byte("stale-source")
	buf := make([]byte, packet.GeneveFixedHeaderLength+len(payload))
	copy(buf[packet.GeneveFixedHeaderLength:], payload)
	staleSource := sourceRxMeta{socketID: sourceIPv4SocketID, generation: 10}
	err := c.sourcePathWriteWireGuardBatchTo(staleSource, epAddr{ap: udpConnAddrPort(t, dstConn.LocalAddr())}, [][]byte{buf}, packet.GeneveFixedHeaderLength)
	if !errors.Is(err, errSourcePathUnavailable) {
		t.Fatalf("sourcePathWriteWireGuardBatchTo error = %v, want %v", err, errSourcePathUnavailable)
	}
}

func TestSendUDPBatchFromSourceAuxErrorDoesNotRebind(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	})

	tests := []struct {
		name        string
		dst         string
		local       *net.UDPAddr
		socketID    SourceSocketID
		bindAuxConn func(*Conn, nettype.PacketConn)
	}{
		{
			name:     "ipv4",
			dst:      "192.0.2.1:41641",
			local:    &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345},
			socketID: sourceIPv4SocketID,
			bindAuxConn: func(c *Conn, pc nettype.PacketConn) {
				c.sourcePath.aux4.setID(sourceIPv4SocketID)
				c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
				c.sourcePath.aux4.pconn.mu.Lock()
				c.sourcePath.aux4.pconn.setConnLocked(pc, "udp4", 1)
				c.sourcePath.aux4.pconn.mu.Unlock()
				c.sourcePath.aux4Bound = true
			},
		},
		{
			name:     "ipv6",
			dst:      "[2001:db8::1]:41641",
			local:    &net.UDPAddr{IP: net.ParseIP("::1"), Port: 12345},
			socketID: sourceIPv6SocketID,
			bindAuxConn: func(c *Conn, pc nettype.PacketConn) {
				c.sourcePath.aux6.setID(sourceIPv6SocketID)
				c.sourcePath.aux6.generation.Store(uint64(c.sourcePath.generation))
				c.sourcePath.aux6.pconn.mu.Lock()
				c.sourcePath.aux6.pconn.setConnLocked(pc, "udp6", 1)
				c.sourcePath.aux6.pconn.mu.Unlock()
				c.sourcePath.aux6Bound = true
			},
		},
	}

	sendErr := &net.OpError{Err: syscall.EPERM}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c Conn
			c.sourcePath.generation = 13
			sentinel := time.Unix(123, 0)
			c.lastErrRebind.Store(sentinel)
			tt.bindAuxConn(&c, &failingSourcePathPacketConn{
				local: tt.local,
				err:   sendErr,
			})

			dst := epAddr{ap: netip.MustParseAddrPort(tt.dst)}
			source := c.sourcePathDataSendSource(dst)
			if source.socketID != tt.socketID {
				t.Fatalf("selected source socket = %d, want %d", source.socketID, tt.socketID)
			}

			payload := []byte("source-path-send-error-" + tt.name)
			buf := make([]byte, packet.GeneveFixedHeaderLength+len(payload))
			copy(buf[packet.GeneveFixedHeaderLength:], payload)
			sent, err := c.sendUDPBatchFromSource(source, dst, [][]byte{buf}, packet.GeneveFixedHeaderLength)
			if sent {
				t.Fatal("sendUDPBatchFromSource reported sent packet on auxiliary send error")
			}
			if !errors.Is(err, syscall.EPERM) {
				t.Fatalf("sendUDPBatchFromSource error = %v, want %v", err, syscall.EPERM)
			}
			if got := c.lastErrRebind.Load(); !got.Equal(sentinel) {
				t.Fatalf("lastErrRebind = %v, want unchanged sentinel %v", got, sentinel)
			}
		})
	}
}

func TestSourcePathDualSendSendsPrimaryAndAux(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
	})

	dstConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	primaryConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	auxConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")

	var c Conn
	c.sourcePath.generation = 23
	c.pconn4.mu.Lock()
	c.pconn4.setConnLocked(primaryConn, "udp4", 1)
	c.pconn4.mu.Unlock()
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4.pconn.mu.Lock()
	c.sourcePath.aux4.pconn.setConnLocked(auxConn, "udp4", 1)
	c.sourcePath.aux4.pconn.mu.Unlock()
	c.sourcePath.aux4Bound = true

	dst := epAddr{ap: udpConnAddrPort(t, dstConn.LocalAddr())}
	auxSource := c.sourcePath.aux4.rxMeta()
	seedSourcePathSamples(t, &c, dst, auxSource)

	source, ok := c.sourcePathDualSendCandidate(dst)
	if !ok {
		t.Fatal("sourcePathDualSendCandidate did not select aux")
	}
	if source != auxSource {
		t.Fatalf("dual-send source = %+v, want %+v", source, auxSource)
	}

	payload := []byte("source-path-dual-send")
	buf := make([]byte, packet.GeneveFixedHeaderLength+len(payload))
	copy(buf[packet.GeneveFixedHeaderLength:], payload)
	primaryPacketsBefore := metricSourcePathDualSendPrimaryPackets.Value()
	auxPacketsBefore := metricSourcePathDualSendAuxPackets.Value()
	res := c.sendUDPBatchDualSource(source, dst, [][]byte{buf}, packet.GeneveFixedHeaderLength)
	if res.err != nil {
		t.Fatalf("sendUDPBatchDualSource error = %v", res.err)
	}
	if res.primaryErr != nil {
		t.Fatalf("primary send error = %v", res.primaryErr)
	}
	if res.auxErr != nil {
		t.Fatalf("aux send error = %v", res.auxErr)
	}
	if got := metricSourcePathDualSendPrimaryPackets.Value() - primaryPacketsBefore; got != 1 {
		t.Fatalf("dual-send primary packets metric delta = %d, want 1", got)
	}
	if got := metricSourcePathDualSendAuxPackets.Value() - auxPacketsBefore; got != 1 {
		t.Fatalf("dual-send aux packets metric delta = %d, want 1", got)
	}

	wantPrimary := udpConnAddrPort(t, primaryConn.LocalAddr())
	wantAux := udpConnAddrPort(t, auxConn.LocalAddr())
	seen := map[uint16]bool{}
	for i := 0; i < 2; i++ {
		if err := dstConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 128)
		n, src, err := dstConn.ReadFromUDPAddrPort(got)
		if err != nil {
			t.Fatalf("ReadFromUDPAddrPort #%d: %v", i+1, err)
		}
		if string(got[:n]) != string(payload) {
			t.Fatalf("received payload %q, want %q", got[:n], payload)
		}
		seen[src.Port()] = true
	}
	if !seen[wantPrimary.Port()] {
		t.Fatalf("primary source port %d was not seen; seen=%v", wantPrimary.Port(), seen)
	}
	if !seen[wantAux.Port()] {
		t.Fatalf("aux source port %d was not seen; seen=%v", wantAux.Port(), seen)
	}
}

func TestSourcePathDualSendUsesBoundAuxWithoutProbeSample(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
	})

	var c Conn
	c.sourcePath.generation = 23
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true

	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	want := c.sourcePath.aux4.rxMeta()
	got, ok := c.sourcePathDualSendCandidate(dst)
	if !ok {
		t.Fatal("sourcePathDualSendCandidate rejected bound aux without probe sample")
	}
	if got != want {
		t.Fatalf("dual-send source = %+v, want %+v", got, want)
	}
}

func TestSourcePathDualSendChoosesLowestLatencyAuxSocket(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "2")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
	})

	var c Conn
	c.sourcePath.generation = 31
	c.sourcePath.mu.Lock()
	c.ensureSourcePathAuxSocketCountLocked(2)
	c.forEachSourcePathSocketLocked(true, func(_ int, sock *sourcePathSocket, bound *bool) {
		sock.generation.Store(uint64(c.sourcePath.generation))
		*bound = true
	})
	c.sourcePath.mu.Unlock()

	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	sources := c.sourcePathProbeSources(true)
	if len(sources) != 2 {
		t.Fatalf("IPv4 probe sources = %+v, want two candidates", sources)
	}
	slow, fast := sources[0], sources[1]
	now := mono.Now()
	c.mu.Lock()
	for i := 0; i < sourcePathMinSamplesForUse; i++ {
		c.sourceProbes.samples = append(c.sourceProbes.samples,
			sourcePathProbeSample{
				dst:     dst,
				source:  slow,
				latency: 40 * time.Millisecond,
				at:      now.Add(-time.Duration(i) * time.Millisecond),
			},
			sourcePathProbeSample{
				dst:     dst,
				source:  fast,
				latency: 10 * time.Millisecond,
				at:      now.Add(-time.Duration(i) * time.Millisecond),
			},
		)
	}
	c.mu.Unlock()

	got, ok := c.sourcePathDualSendCandidate(dst)
	if !ok {
		t.Fatal("sourcePathDualSendCandidate rejected warmed aux candidates")
	}
	if got != fast {
		t.Fatalf("dual-send source = %+v, want lowest-latency candidate %+v", got, fast)
	}
}

func TestSourcePathDualSendWritesViaLowestLatencyExtraAuxSocket(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "2")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
	})

	dstConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	primaryConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	slowAuxConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	fastAuxConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")

	var c Conn
	c.sourcePath.generation = 32
	c.pconn4.mu.Lock()
	c.pconn4.setConnLocked(primaryConn, "udp4", 1)
	c.pconn4.mu.Unlock()
	c.sourcePath.mu.Lock()
	c.ensureSourcePathAuxSocketCountLocked(2)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4.pconn.mu.Lock()
	c.sourcePath.aux4.pconn.setConnLocked(slowAuxConn, "udp4", 1)
	c.sourcePath.aux4.pconn.mu.Unlock()
	c.sourcePath.aux4Bound = true
	c.sourcePath.extraAux4[0].generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.extraAux4[0].pconn.mu.Lock()
	c.sourcePath.extraAux4[0].pconn.setConnLocked(fastAuxConn, "udp4", 1)
	c.sourcePath.extraAux4[0].pconn.mu.Unlock()
	c.sourcePath.extra4Bound[0] = true
	c.sourcePath.mu.Unlock()

	dst := epAddr{ap: udpConnAddrPort(t, dstConn.LocalAddr())}
	sources := c.sourcePathProbeSources(true)
	slow, fast := sources[0], sources[1]
	now := mono.Now()
	c.mu.Lock()
	for i := 0; i < sourcePathMinSamplesForUse; i++ {
		c.sourceProbes.samples = append(c.sourceProbes.samples,
			sourcePathProbeSample{
				dst:     dst,
				source:  slow,
				latency: 50 * time.Millisecond,
				at:      now.Add(-time.Duration(i) * time.Millisecond),
			},
			sourcePathProbeSample{
				dst:     dst,
				source:  fast,
				latency: 5 * time.Millisecond,
				at:      now.Add(-time.Duration(i) * time.Millisecond),
			},
		)
	}
	c.mu.Unlock()

	source, ok := c.sourcePathDualSendCandidate(dst)
	if !ok {
		t.Fatal("sourcePathDualSendCandidate rejected warmed aux candidates")
	}
	if source != fast {
		t.Fatalf("dual-send source = %+v, want extra aux %+v", source, fast)
	}

	payload := []byte("source-path-dual-send-extra-aux")
	buf := make([]byte, packet.GeneveFixedHeaderLength+len(payload))
	copy(buf[packet.GeneveFixedHeaderLength:], payload)
	res := c.sendUDPBatchDualSource(source, dst, [][]byte{buf}, packet.GeneveFixedHeaderLength)
	if res.err != nil {
		t.Fatalf("sendUDPBatchDualSource error = %v", res.err)
	}
	if res.primaryErr != nil {
		t.Fatalf("primary send error = %v", res.primaryErr)
	}
	if res.auxErr != nil {
		t.Fatalf("aux send error = %v", res.auxErr)
	}

	wantPrimary := udpConnAddrPort(t, primaryConn.LocalAddr())
	wantFastAux := udpConnAddrPort(t, fastAuxConn.LocalAddr())
	wantSlowAux := udpConnAddrPort(t, slowAuxConn.LocalAddr())
	seen := map[uint16]bool{}
	for i := 0; i < 2; i++ {
		if err := dstConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 128)
		n, src, err := dstConn.ReadFromUDPAddrPort(got)
		if err != nil {
			t.Fatalf("ReadFromUDPAddrPort #%d: %v", i+1, err)
		}
		if string(got[:n]) != string(payload) {
			t.Fatalf("received payload %q, want %q", got[:n], payload)
		}
		seen[src.Port()] = true
	}
	if !seen[wantPrimary.Port()] {
		t.Fatalf("primary source port %d was not seen; seen=%v", wantPrimary.Port(), seen)
	}
	if !seen[wantFastAux.Port()] {
		t.Fatalf("fast aux source port %d was not seen; seen=%v", wantFastAux.Port(), seen)
	}
	if seen[wantSlowAux.Port()] {
		t.Fatalf("slow aux source port %d was used; seen=%v", wantSlowAux.Port(), seen)
	}
}

func TestSourcePathDualSendIgnoresPrimaryBeatGate(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_MULTI_METRIC", "")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PROFILE", "")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_MULTI_METRIC", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_PROFILE", "")
	})

	var c Conn
	c.peerMap = newPeerMap()
	c.sourcePath.generation = 26
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true

	dst := epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}
	aux := c.sourcePath.aux4.rxMeta()
	now := mono.Now()
	c.mu.Lock()
	for i := 0; i < sourcePathMinSamplesForUse; i++ {
		c.sourceProbes.samples = append(c.sourceProbes.samples, sourcePathProbeSample{
			dst:     dst,
			source:  aux,
			latency: 20 * time.Millisecond,
			at:      now.Add(-time.Duration(i) * time.Millisecond),
		})
	}
	c.mu.Unlock()

	peerKey := key.NewNode().Public()
	state := &endpointState{}
	state.addPongReplyLocked(pongReply{latency: 5 * time.Millisecond, pongAt: now})
	de := &endpoint{
		c:             &c,
		publicKey:     peerKey,
		endpointState: map[netip.AddrPort]*endpointState{dst.ap: state},
	}
	c.mu.Lock()
	c.peerMap.byNodeKey[peerKey] = newPeerInfo(de)
	c.peerMap.setNodeKeyForEpAddr(dst, peerKey)
	c.mu.Unlock()

	if _, ok := c.sourcePathBestCandidate(dst); ok {
		t.Fatal("auto source-path candidate selected aux that does not beat primary")
	}
	source, ok := c.sourcePathDualSendCandidate(dst)
	if !ok {
		t.Fatal("dual-send candidate rejected aux solely because primary was faster")
	}
	if source != aux {
		t.Fatalf("dual-send source = %+v, want %+v", source, aux)
	}
}

func TestSourcePathDualSendAuxErrorRebindsAuxSocket(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
	})

	dstConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	primaryConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	failingAux := &failingSourcePathPacketConn{
		local: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345},
		err:   syscall.EPERM,
	}

	var c Conn
	c.testOnlyPacketListener = localhostListener{}
	c.sourcePath.generation = 24
	c.pconn4.mu.Lock()
	c.pconn4.setConnLocked(primaryConn, "udp4", 1)
	c.pconn4.mu.Unlock()
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4.pconn.mu.Lock()
	c.sourcePath.aux4.pconn.setConnLocked(failingAux, "udp4", 1)
	c.sourcePath.aux4.pconn.mu.Unlock()
	c.sourcePath.aux4Bound = true

	dst := epAddr{ap: udpConnAddrPort(t, dstConn.LocalAddr())}
	auxSource := c.sourcePath.aux4.rxMeta()
	seedSourcePathSamples(t, &c, dst, auxSource)

	payload := []byte("source-path-dual-send-aux-error")
	buf := make([]byte, packet.GeneveFixedHeaderLength+len(payload))
	copy(buf[packet.GeneveFixedHeaderLength:], payload)
	rebindsBefore := metricSourcePathDualSendAuxRebinds.Value()
	source, ok := c.sourcePathDualSendCandidate(dst)
	if !ok {
		t.Fatal("dual-send candidate unexpectedly unavailable before aux failure")
	}
	res := c.sendUDPBatchDualSource(source, dst, [][]byte{buf}, packet.GeneveFixedHeaderLength)
	if res.err != nil {
		t.Fatalf("sendUDPBatchDualSource error after primary success = %v", res.err)
	}
	if res.primaryErr != nil {
		t.Fatalf("primary send error = %v", res.primaryErr)
	}
	if !errors.Is(res.auxErr, syscall.EPERM) {
		t.Fatalf("aux send error = %v, want %v", res.auxErr, syscall.EPERM)
	}

	c.mu.Lock()
	sampleCount := len(c.sourceProbes.samples)
	c.mu.Unlock()
	if sampleCount != 0 {
		t.Fatalf("source-path samples = %d, want old aux samples dropped after rebind", sampleCount)
	}
	if !failingAux.closed {
		t.Fatal("old failing aux socket was not closed during rebind")
	}
	newSource := c.sourcePath.aux4.rxMeta()
	if newSource == auxSource {
		t.Fatalf("aux source did not rotate: got %+v", newSource)
	}
	if got := metricSourcePathDualSendAuxRebinds.Value() - rebindsBefore; got != 1 {
		t.Fatalf("aux rebind metric delta = %d, want 1", got)
	}
	source, ok = c.sourcePathDualSendCandidate(dst)
	if !ok {
		t.Fatal("dual-send candidate unavailable after aux socket rebind")
	}
	if source != newSource {
		t.Fatalf("dual-send source after rebind = %+v, want %+v", source, newSource)
	}
}

func TestSourcePathProbeTimeoutRebindsPreviouslyWorkingAuxSocket(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_AUX_PROBE_DROP_STREAK", "1")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_AUX_PROBE_DROP_STREAK", "")
	})

	auxConn := listenUDPForSourcePathTest(t, "udp4", "127.0.0.1:0")
	var c Conn
	c.testOnlyPacketListener = localhostListener{}
	c.sourcePath.generation = 31
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4.pconn.mu.Lock()
	c.sourcePath.aux4.pconn.setConnLocked(auxConn, "udp4", 1)
	c.sourcePath.aux4.pconn.mu.Unlock()
	c.sourcePath.aux4Bound = true

	dst := epAddr{ap: netip.MustParseAddrPort("127.0.0.1:41641")}
	oldSource := c.sourcePath.aux4.rxMeta()
	now := mono.Now()
	c.mu.Lock()
	c.sourceProbes.samples = append(c.sourceProbes.samples, sourcePathProbeSample{
		dst:     dst,
		source:  oldSource,
		latency: time.Millisecond,
		at:      now,
	})
	rotations := c.sourceProbes.noteProbeExpirationsLocked([]sourcePathProbeTx{
		{dst: dst, source: oldSource, at: now.Add(-pingTimeoutDuration)},
	}, now)
	c.mu.Unlock()

	if len(rotations) != 1 {
		t.Fatalf("probe timeout rotations = %d, want 1", len(rotations))
	}
	rebindsBefore := metricSourcePathDualSendAuxRebinds.Value()
	c.rotateSourcePathAuxSocket(rotations[0].dst, rotations[0].source, rotations[0].reason, nil)

	newSource := c.sourcePath.aux4.rxMeta()
	if newSource == oldSource {
		t.Fatalf("aux source did not rotate after probe timeout: %+v", newSource)
	}
	if got := metricSourcePathDualSendAuxRebinds.Value() - rebindsBefore; got != 1 {
		t.Fatalf("aux rebind metric delta = %d, want 1", got)
	}
}

func TestSourcePathForcedAuxDualNodeRuntime(t *testing.T) {
	tests := []struct {
		name  string
		want4 bool
	}{
		{"IPv4", true},
		{"IPv6", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.want4 {
				if err := ipv6LoopbackUDPRoundtripProbe(); err != nil {
					t.Skipf("IPv6 loopback UDP roundtrip not delivered on this host (%v); srcsel IPv6 paths must be validated on a host with working IPv6 loopback or via real-network tests", err)
				}
			}
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
			t.Cleanup(func() {
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
			})

			logf := logger.WithPrefix(t.Logf, "srcsel-dual-node-"+tt.name+": ")
			pl := &recordingPacketListener{
				base:     localhostListener{},
				failures: map[netip.AddrPort]error{},
			}
			dm, cleanupDERP := runDERPAndStun(t, logf, pl, netip.MustParseAddr("127.0.0.1"))
			t.Cleanup(cleanupDERP)
			m1 := newMagicStack(t, logger.WithPrefix(logf, "m1: "), pl, dm)
			t.Cleanup(m1.Close)
			m2 := newMagicStack(t, logger.WithPrefix(logf, "m2: "), pl, dm)
			t.Cleanup(m2.Close)

			m1Primary, ok := primaryUDPAddrPort(t, m1.conn, tt.want4)
			if !ok {
				if tt.want4 {
					t.Fatal("IPv4 primary socket was not bound for m1")
				}
				t.Skip("IPv6 primary socket was not bound for m1")
			}
			m2Primary, ok := primaryUDPAddrPort(t, m2.conn, tt.want4)
			if !ok {
				if tt.want4 {
					t.Fatal("IPv4 primary socket was not bound for m2")
				}
				t.Skip("IPv6 primary socket was not bound for m2")
			}

			cleanupMesh := meshStacks(logf, sourcePathRuntimeNetmapEndpoints(t, []netip.AddrPort{m1Primary, m2Primary}), m1, m2)
			t.Cleanup(cleanupMesh)

			cleanupPing := newPinger(t, logf, m1, m2)
			mustDirect(t, logf, m1, m2)
			mustDirect(t, logf, m2, m1)
			cleanupPing()

			auxLocal := waitForSourcePathAuxLocal(t, m1.conn, tt.want4)
			primaryLocal := m1Primary
			directPeer := currentDirectPeerAddr(t, m1, m2, tt.want4)
			t.Logf("forced aux runtime path: aux=%v primary=%v peer=%v", auxLocal, primaryLocal, directPeer)

			sentinel := time.Unix(123, 0)
			m1.conn.lastErrRebind.Store(sentinel)
			selectedBefore := metricSourcePathDataSendAuxSelected.Value()
			succeededBefore := metricSourcePathDataSendAuxSucceeded.Value()
			pl.clearWrites()
			transitOnePing(t, m1, m2)
			writes := pl.writesSnapshot()
			if !hasWireGuardWrite(writes, auxLocal, directPeer, false) {
				t.Fatalf("forced aux send did not emit a WireGuard UDP packet from aux %v to peer %v; writes=%v", auxLocal, directPeer, summarizeWrites(writes))
			}
			if got := metricSourcePathDataSendAuxSelected.Value() - selectedBefore; got < 1 {
				t.Fatalf("forced aux send selected metric delta = %d, want at least 1", got)
			}
			if got := metricSourcePathDataSendAuxSucceeded.Value() - succeededBefore; got < 1 {
				t.Fatalf("forced aux send succeeded metric delta = %d, want at least 1", got)
			}
			if got := m1.conn.lastErrRebind.Load(); !got.Equal(sentinel) {
				t.Fatalf("successful aux send updated lastErrRebind: got %v want %v", got, sentinel)
			}

			pl.setFailure(auxLocal, &net.OpError{Op: "write", Err: syscall.EPERM})
			t.Cleanup(pl.clearFailures)
			selectedBefore = metricSourcePathDataSendAuxSelected.Value()
			fallbackBefore := metricSourcePathDataSendAuxFallback.Value()
			pl.clearWrites()
			transitOnePing(t, m1, m2)
			writes = pl.writesSnapshot()
			if !hasWireGuardWrite(writes, auxLocal, directPeer, true) {
				t.Fatalf("forced aux failure did not try aux %v to peer %v first; writes=%v", auxLocal, directPeer, summarizeWrites(writes))
			}
			if !hasWireGuardWrite(writes, primaryLocal, directPeer, false) {
				t.Fatalf("forced aux failure did not fall back to primary %v to peer %v; writes=%v", primaryLocal, directPeer, summarizeWrites(writes))
			}
			if got := metricSourcePathDataSendAuxSelected.Value() - selectedBefore; got < 1 {
				t.Fatalf("forced aux fallback selected metric delta = %d, want at least 1", got)
			}
			if got := metricSourcePathDataSendAuxFallback.Value() - fallbackBefore; got < 1 {
				t.Fatalf("forced aux fallback metric delta = %d, want at least 1", got)
			}
			if got := m1.conn.lastErrRebind.Load(); !got.Equal(sentinel) {
				t.Fatalf("aux fallback updated lastErrRebind: got %v want %v", got, sentinel)
			}
		})
	}
}

func TestSourcePathAutomaticAuxDualNodeRuntime(t *testing.T) {
	tests := []struct {
		name  string
		want4 bool
	}{
		{"IPv4", true},
		{"IPv6", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.want4 {
				if err := ipv6LoopbackUDPRoundtripProbe(); err != nil {
					t.Skipf("IPv6 loopback UDP roundtrip not delivered on this host (%v); srcsel IPv6 paths must be validated on a host with working IPv6 loopback or via real-network tests", err)
				}
			}
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "single-source")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "true")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "false")
			// On loopback the real primary RTT is sub-millisecond, far below
			// the seeded 1ms aux samples. Disable the primary-baseline gate
			// so this test exercises automatic-mode selection logic, not
			// Phase 20's relative-improvement check.
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT", "-1")
			t.Cleanup(func() {
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT", "")
			})

			logf := logger.WithPrefix(t.Logf, "srcsel-auto-dual-node-"+tt.name+": ")
			pl := &recordingPacketListener{
				base:     localhostListener{},
				failures: map[netip.AddrPort]error{},
			}
			dm, cleanupDERP := runDERPAndStun(t, logf, pl, netip.MustParseAddr("127.0.0.1"))
			t.Cleanup(cleanupDERP)
			m1 := newMagicStack(t, logger.WithPrefix(logf, "m1: "), pl, dm)
			t.Cleanup(m1.Close)
			m2 := newMagicStack(t, logger.WithPrefix(logf, "m2: "), pl, dm)
			t.Cleanup(m2.Close)

			m1Primary, ok := primaryUDPAddrPort(t, m1.conn, tt.want4)
			if !ok {
				if tt.want4 {
					t.Fatal("IPv4 primary socket was not bound for m1")
				}
				t.Skip("IPv6 primary socket was not bound for m1")
			}
			m2Primary, ok := primaryUDPAddrPort(t, m2.conn, tt.want4)
			if !ok {
				if tt.want4 {
					t.Fatal("IPv4 primary socket was not bound for m2")
				}
				t.Skip("IPv6 primary socket was not bound for m2")
			}

			cleanupMesh := meshStacks(logf, sourcePathRuntimeNetmapEndpoints(t, []netip.AddrPort{m1Primary, m2Primary}), m1, m2)
			t.Cleanup(cleanupMesh)

			cleanupPing := newPinger(t, logf, m1, m2)
			mustDirect(t, logf, m1, m2)
			mustDirect(t, logf, m2, m1)
			cleanupPing()

			auxLocal := waitForSourcePathAuxLocal(t, m1.conn, tt.want4)
			primaryLocal := m1Primary
			directPeer := currentDirectPeerAddr(t, m1, m2, tt.want4)
			directDst := epAddr{ap: directPeer}
			selected := seedSourcePathAutomaticCandidate(t, m1.conn, directDst, tt.want4)
			if got := m1.conn.sourcePathDataSendSource(directDst); got != selected {
				t.Fatalf("automatic runtime selected source = %+v, want seeded candidate %+v", got, selected)
			}
			t.Logf("automatic aux runtime path: aux=%v primary=%v peer=%v source=%+v", auxLocal, primaryLocal, directPeer, selected)

			sentinel := time.Unix(456, 0)
			m1.conn.lastErrRebind.Store(sentinel)
			selectedBefore := metricSourcePathDataSendAuxSelected.Value()
			succeededBefore := metricSourcePathDataSendAuxSucceeded.Value()
			pl.clearWrites()
			transitOnePing(t, m1, m2)
			writes := pl.writesSnapshot()
			if !hasWireGuardWrite(writes, auxLocal, directPeer, false) {
				t.Fatalf("automatic aux send did not emit a WireGuard UDP packet from aux %v to peer %v; writes=%v", auxLocal, directPeer, summarizeWrites(writes))
			}
			if got := metricSourcePathDataSendAuxSelected.Value() - selectedBefore; got < 1 {
				t.Fatalf("automatic aux send selected metric delta = %d, want at least 1", got)
			}
			if got := metricSourcePathDataSendAuxSucceeded.Value() - succeededBefore; got < 1 {
				t.Fatalf("automatic aux send succeeded metric delta = %d, want at least 1", got)
			}
			if got := m1.conn.lastErrRebind.Load(); !got.Equal(sentinel) {
				t.Fatalf("successful automatic aux send updated lastErrRebind: got %v want %v", got, sentinel)
			}

			pl.setFailure(auxLocal, &net.OpError{Op: "write", Err: syscall.EPERM})
			t.Cleanup(pl.clearFailures)
			selectedBefore = metricSourcePathDataSendAuxSelected.Value()
			fallbackBefore := metricSourcePathDataSendAuxFallback.Value()
			pl.clearWrites()
			transitOnePing(t, m1, m2)
			writes = pl.writesSnapshot()
			if !hasWireGuardWrite(writes, auxLocal, directPeer, true) {
				t.Fatalf("automatic aux failure did not try aux %v to peer %v first; writes=%v", auxLocal, directPeer, summarizeWrites(writes))
			}
			if !hasWireGuardWrite(writes, primaryLocal, directPeer, false) {
				t.Fatalf("automatic aux failure did not fall back to primary %v to peer %v; writes=%v", primaryLocal, directPeer, summarizeWrites(writes))
			}
			if got := metricSourcePathDataSendAuxSelected.Value() - selectedBefore; got < 1 {
				t.Fatalf("automatic aux fallback selected metric delta = %d, want at least 1", got)
			}
			if got := metricSourcePathDataSendAuxFallback.Value() - fallbackBefore; got < 1 {
				t.Fatalf("automatic aux fallback metric delta = %d, want at least 1", got)
			}
			if got := m1.conn.lastErrRebind.Load(); !got.Equal(sentinel) {
				t.Fatalf("automatic aux fallback updated lastErrRebind: got %v want %v", got, sentinel)
			}
		})
	}
}

type failingSourcePathPacketConn struct {
	local  *net.UDPAddr
	err    error
	closed bool
}

func (c *failingSourcePathPacketConn) WriteToUDPAddrPort([]byte, netip.AddrPort) (int, error) {
	return 0, c.err
}

func (c *failingSourcePathPacketConn) ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error) {
	return 0, netip.AddrPort{}, c.err
}

func (c *failingSourcePathPacketConn) Close() error {
	c.closed = true
	return nil
}

func (c *failingSourcePathPacketConn) LocalAddr() net.Addr {
	return c.local
}

func (c *failingSourcePathPacketConn) SetDeadline(time.Time) error {
	return nil
}

func (c *failingSourcePathPacketConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *failingSourcePathPacketConn) SetWriteDeadline(time.Time) error {
	return nil
}

func listenUDPForSourcePathTest(t *testing.T, network, addr string) *net.UDPConn {
	t.Helper()
	if network == "udp6" {
		if err := ipv6LoopbackUDPRoundtripProbe(); err != nil {
			t.Skipf("IPv6 loopback UDP roundtrip not delivered on this host (%v); srcsel IPv6 paths must be validated on a host with working IPv6 loopback or via real-network tests", err)
		}
	}
	ua, err := net.ResolveUDPAddr(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.ListenUDP(network, ua)
	if err != nil {
		if network == "udp6" {
			t.Skipf("IPv6 loopback UDP unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func readUDPConnPayload(t *testing.T, c *net.UDPConn, want []byte) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 128)
	n, _, err := c.ReadFromUDPAddrPort(got)
	if err != nil {
		t.Fatalf("ReadFromUDPAddrPort: %v", err)
	}
	if string(got[:n]) != string(want) {
		t.Fatalf("received payload %q, want %q", got[:n], want)
	}
}

func assertNoUDPConnPayload(t *testing.T, c *net.UDPConn) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 128)
	_, _, err := c.ReadFromUDPAddrPort(got)
	if err == nil {
		t.Fatal("unexpected UDP payload")
	}
	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("ReadFromUDPAddrPort error = %v, want timeout", err)
	}
}

var (
	ipv6LoopbackUDPProbeOnce sync.Once
	ipv6LoopbackUDPProbeErr  error
)

// ipv6LoopbackUDPRoundtripProbe verifies that a UDP datagram sent to [::1] on
// this host is actually delivered to a peer socket on [::1]. The result is
// cached for the lifetime of the test binary.
//
// Some Windows hosts (for example a Windows Server with WSL2 / Hyper-V
// firewall) silently drop UDP traffic to ::1 even though net.ListenUDP
// succeeds. On such hosts the source-path IPv6 loopback tests cannot complete
// regardless of srcsel correctness, so the probe is used by
// listenUDPForSourcePathTest to skip those tests with an explanatory message.
func ipv6LoopbackUDPRoundtripProbe() error {
	ipv6LoopbackUDPProbeOnce.Do(func() {
		dst, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
		if err != nil {
			ipv6LoopbackUDPProbeErr = err
			return
		}
		defer dst.Close()
		src, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
		if err != nil {
			ipv6LoopbackUDPProbeErr = err
			return
		}
		defer src.Close()
		dstAP, err := netip.ParseAddrPort(dst.LocalAddr().String())
		if err != nil {
			ipv6LoopbackUDPProbeErr = err
			return
		}
		if _, err := src.WriteToUDPAddrPort([]byte{0}, dstAP); err != nil {
			ipv6LoopbackUDPProbeErr = err
			return
		}
		if err := dst.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			ipv6LoopbackUDPProbeErr = err
			return
		}
		var buf [4]byte
		if _, _, err := dst.ReadFromUDPAddrPort(buf[:]); err != nil {
			ipv6LoopbackUDPProbeErr = err
			return
		}
	})
	return ipv6LoopbackUDPProbeErr
}

func udpConnAddrPort(t *testing.T, addr net.Addr) netip.AddrPort {
	t.Helper()
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		t.Fatalf("ParseAddrPort(%q): %v", addr.String(), err)
	}
	return ap
}

func seedSourcePathSamples(t *testing.T, c *Conn, dst epAddr, source sourceRxMeta) {
	t.Helper()
	now := mono.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := 0; i < sourcePathMinSamplesForUse; i++ {
		c.sourceProbes.samples = append(c.sourceProbes.samples, sourcePathProbeSample{
			dst:     dst,
			source:  source,
			latency: 10 * time.Millisecond,
			at:      now.Add(-time.Duration(i) * time.Millisecond),
		})
	}
}

type recordedUDPWrite struct {
	local   netip.AddrPort
	dst     netip.AddrPort
	payload []byte
	n       int
	err     error
}

type recordingPacketListener struct {
	base nettype.PacketListener

	mu       sync.Mutex
	writes   []recordedUDPWrite
	failures map[netip.AddrPort]error
}

func (pl *recordingPacketListener) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	pc, err := pl.base.ListenPacket(ctx, network, address)
	if err != nil {
		return nil, err
	}
	netipPC, ok := pc.(nettype.PacketConn)
	if !ok {
		pc.Close()
		return nil, errors.New("packet listener returned a connection without netip PacketConn methods")
	}
	return &recordingPacketConn{
		PacketConn: pc,
		netipPC:    netipPC,
		owner:      pl,
	}, nil
}

func (pl *recordingPacketListener) record(write recordedUDPWrite) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.writes = append(pl.writes, write)
}

func (pl *recordingPacketListener) setFailure(local netip.AddrPort, err error) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if pl.failures == nil {
		pl.failures = map[netip.AddrPort]error{}
	}
	pl.failures[local] = err
}

func (pl *recordingPacketListener) clearFailures() {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	clear(pl.failures)
}

func (pl *recordingPacketListener) failureFor(local netip.AddrPort) error {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.failures[local]
}

func (pl *recordingPacketListener) clearWrites() {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.writes = nil
}

func (pl *recordingPacketListener) writesSnapshot() []recordedUDPWrite {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return append([]recordedUDPWrite(nil), pl.writes...)
}

type recordingPacketConn struct {
	net.PacketConn
	netipPC nettype.PacketConn
	owner   *recordingPacketListener
}

func (pc *recordingPacketConn) ReadFromUDPAddrPort(b []byte) (int, netip.AddrPort, error) {
	return pc.netipPC.ReadFromUDPAddrPort(b)
}

func (pc *recordingPacketConn) WriteToUDPAddrPort(b []byte, dst netip.AddrPort) (int, error) {
	local, _ := addrPortFromNetAddr(pc.LocalAddr())
	payload := append([]byte(nil), b...)
	if err := pc.owner.failureFor(local); err != nil {
		pc.owner.record(recordedUDPWrite{local: local, dst: dst, payload: payload, err: err})
		return 0, err
	}
	n, err := pc.netipPC.WriteToUDPAddrPort(b, dst)
	pc.owner.record(recordedUDPWrite{local: local, dst: dst, payload: payload, n: n, err: err})
	return n, err
}

func addrPortFromNetAddr(addr net.Addr) (netip.AddrPort, bool) {
	if addr == nil {
		return netip.AddrPort{}, false
	}
	ap, err := netip.ParseAddrPort(addr.String())
	return ap, err == nil
}

func waitForSourcePathAuxLocal(t *testing.T, c *Conn, want4 bool) netip.AddrPort {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var addr net.Addr
		c.sourcePath.mu.Lock()
		if want4 {
			if c.sourcePath.aux4Bound {
				addr = c.sourcePath.aux4.pconn.LocalAddr()
			}
		} else {
			if c.sourcePath.aux6Bound {
				addr = c.sourcePath.aux6.pconn.LocalAddr()
			}
		}
		c.sourcePath.mu.Unlock()
		if addr != nil {
			return udpConnAddrPort(t, addr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !want4 {
		t.Skip("IPv6 aux source path socket was not bound on this host")
	}
	t.Fatal("IPv4 aux source path socket was not bound")
	return netip.AddrPort{}
}

func primaryUDPAddrPort(t *testing.T, c *Conn, want4 bool) (netip.AddrPort, bool) {
	t.Helper()
	var addr net.Addr
	if want4 {
		addr = c.pconn4.LocalAddr()
	} else {
		addr = c.pconn6.LocalAddr()
	}
	ap, ok := addrPortFromNetAddr(addr)
	if !ok || !ap.IsValid() || ap.Addr().Is4() != want4 {
		return netip.AddrPort{}, false
	}
	return ap, true
}

func currentDirectPeerAddr(t *testing.T, src, dst *magicStack, want4 bool) netip.AddrPort {
	t.Helper()
	peer := src.Status().Peer[dst.Public()]
	if peer == nil {
		t.Fatalf("no peer status for %v", dst.Public())
	}
	ap, err := netip.ParseAddrPort(peer.CurAddr)
	if err != nil {
		t.Fatalf("direct peer address %q is not addr:port: %v", peer.CurAddr, err)
	}
	if ap.Addr().Is4() != want4 {
		t.Fatalf("direct peer address family mismatch: got %v want IPv4=%v", ap, want4)
	}
	return ap
}

func seedSourcePathAutomaticCandidate(t *testing.T, c *Conn, dst epAddr, want4 bool) sourceRxMeta {
	t.Helper()
	sources := c.sourcePathProbeSources(want4)
	if len(sources) != 1 {
		t.Fatalf("source path probe sources for IPv4=%v = %+v, want one current auxiliary source", want4, sources)
	}
	now := mono.Now()
	c.mu.Lock()
	for i := 0; i < sourcePathMinSamplesForUse; i++ {
		c.sourceProbes.samples = append(c.sourceProbes.samples, sourcePathProbeSample{
			txid:     stun.NewTxID(),
			dst:      dst,
			pongFrom: dst,
			pongSrc:  dst.ap,
			source:   sources[0],
			latency:  time.Millisecond,
			at:       now.Add(-time.Duration(i) * time.Millisecond),
		})
	}
	c.mu.Unlock()
	return sources[0]
}

func sourcePathRuntimeNetmapEndpoints(t *testing.T, primary []netip.AddrPort) func(int, *netmap.NetworkMap) {
	t.Helper()
	return func(idx int, nm *netmap.NetworkMap) {
		for i, peerView := range nm.Peers {
			peerStackIdx := i
			if peerStackIdx >= idx {
				peerStackIdx++
			}
			if peerStackIdx >= len(primary) {
				continue
			}
			peer := peerView.AsStruct()
			peer.Endpoints = []netip.AddrPort{primary[peerStackIdx]}
			nm.Peers[i] = peer.View()
		}
	}
}

func transitOnePing(t *testing.T, src, dst *magicStack) {
	t.Helper()
	ping := tuntest.Ping(dst.IP(), src.IP())
	select {
	case src.tun.Outbound <- ping:
	case <-time.After(time.Second):
		t.Fatal("timeout sending ping to source TUN")
	}
	select {
	case <-dst.tun.Inbound:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for ping on destination TUN")
	}
}

func hasWireGuardWrite(writes []recordedUDPWrite, local, dst netip.AddrPort, wantErr bool) bool {
	for _, write := range writes {
		if write.local != local || write.dst != dst {
			continue
		}
		packetType, _ := packetLooksLike(write.payload)
		if packetType != packetLooksLikeWireGuard {
			continue
		}
		if wantErr {
			if write.err != nil {
				return true
			}
			continue
		}
		if write.err == nil && write.n > 0 {
			return true
		}
	}
	return false
}

func summarizeWrites(writes []recordedUDPWrite) []recordedUDPWrite {
	summary := make([]recordedUDPWrite, 0, len(writes))
	for _, write := range writes {
		summary = append(summary, recordedUDPWrite{
			local: write.local,
			dst:   write.dst,
			n:     write.n,
			err:   write.err,
		})
	}
	return summary
}
