// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux

package magicsock

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tailscale/wireguard-go/tun/tuntest"
	"tailscale.com/envknob"
	"tailscale.com/net/packet"
	"tailscale.com/net/stun"
	"tailscale.com/tstime/mono"
	"tailscale.com/types/logger"
	"tailscale.com/types/netmap"
	"tailscale.com/types/nettype"
)

func TestSourcePathDataSendSourceForcedAuxDualStack(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
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

	if !envknobSrcSelEnable() {
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
		{dst: v4, source: sources4[0], latency: 9 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: v4, source: sources4[0], latency: 7 * time.Millisecond, at: now.Add(-1 * time.Second)},
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
	if score4.latency != 7*time.Millisecond || score4.samples != 2 {
		t.Fatalf("IPv4 observe-only score = latency %v samples %d, want 7ms and 2 samples", score4.latency, score4.samples)
	}
	if !ok6 {
		t.Fatal("IPv6 observe-only candidate not found")
	}
	if score6.source != sources6[0] {
		t.Fatalf("IPv6 observe-only candidate source = %+v, want %+v", score6.source, sources6[0])
	}
	if score6.latency != 11*time.Millisecond || score6.samples != 1 {
		t.Fatalf("IPv6 observe-only score = latency %v samples %d, want 11ms and 1 sample", score6.latency, score6.samples)
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
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
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
		{dst: v4, source: sources4[0], latency: 8 * time.Millisecond, at: now.Add(-2 * time.Second)},
		{dst: v4, source: sources4[0], latency: 6 * time.Millisecond, at: now.Add(-1 * time.Second)},
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
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "true")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
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
		{dst: direct4, source: source4, latency: 6 * time.Millisecond, at: now},
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
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
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
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
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
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
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
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
			t.Cleanup(func() {
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
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
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
			envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "true")
			t.Cleanup(func() {
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
				envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE", "")
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

func udpConnAddrPort(t *testing.T, addr net.Addr) netip.AddrPort {
	t.Helper()
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		t.Fatalf("ParseAddrPort(%q): %v", addr.String(), err)
	}
	return ap
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
	c.sourceProbes.samples = append(c.sourceProbes.samples, sourcePathProbeSample{
		txid:     stun.NewTxID(),
		dst:      dst,
		pongFrom: dst,
		pongSrc:  dst.ap,
		source:   sources[0],
		latency:  time.Millisecond,
		at:       now,
	})
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
