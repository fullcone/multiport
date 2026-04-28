// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux

package magicsock

import (
	"net/netip"
	"testing"

	"tailscale.com/envknob"
)

func TestSourcePathDataSendSourceForcedAuxDualStack(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
	})

	var c Conn
	c.sourcePath.generation = 7
	c.sourcePath.aux4.id = sourceIPv4SocketID
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4Bound = true
	c.sourcePath.aux6.id = sourceIPv6SocketID
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
