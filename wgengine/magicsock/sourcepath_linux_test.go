// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux

package magicsock

import (
	"errors"
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"tailscale.com/envknob"
	"tailscale.com/net/packet"
	"tailscale.com/types/nettype"
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

func TestSendUDPBatchFromSourceAuxDualStackLoopback(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "1")
	envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "aux")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
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
				c.sourcePath.aux4.id = sourceIPv4SocketID
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
				c.sourcePath.aux6.id = sourceIPv6SocketID
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
	c.sourcePath.aux4.id = sourceIPv4SocketID
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
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_ENABLE", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS", "")
		envknob.Setenv("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE", "")
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
				c.sourcePath.aux4.id = sourceIPv4SocketID
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
				c.sourcePath.aux6.id = sourceIPv6SocketID
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

type failingSourcePathPacketConn struct {
	local *net.UDPAddr
	err   error
}

func (c *failingSourcePathPacketConn) WriteToUDPAddrPort([]byte, netip.AddrPort) (int, error) {
	return 0, c.err
}

func (c *failingSourcePathPacketConn) ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error) {
	return 0, netip.AddrPort{}, c.err
}

func (c *failingSourcePathPacketConn) Close() error {
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
