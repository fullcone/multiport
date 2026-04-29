// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux || windows

package magicsock

import (
	"errors"
	"net"
	"net/netip"
	"strings"

	"github.com/tailscale/wireguard-go/conn"
	"tailscale.com/envknob"
)

var (
	envknobSrcSelEnable          = envknob.RegisterBool("TS_EXPERIMENTAL_SRCSEL_ENABLE")
	envknobSrcSelAuxSockets      = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS")
	envknobSrcSelForceDataSource = envknob.RegisterString("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE")
	envknobSrcSelAutoDataSource  = envknob.RegisterBool("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE")
	envknobSrcSelMaxPeers        = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_MAX_PEERS")
	envknobSrcSelMaxProbeBurst   = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST")
)

func sourcePathAuxSocketCount() int {
	if !envknobSrcSelEnable() {
		return 0
	}
	n := envknobSrcSelAuxSockets()
	if n < 0 {
		return 0
	}
	return min(n, 1)
}

func sourcePathProbeMaxPeerCount() int {
	n := envknobSrcSelMaxPeers()
	if n <= 0 {
		return sourcePathProbeMaxPeers
	}
	return n
}

func sourcePathProbeMaxBurstCount() int {
	n := envknobSrcSelMaxProbeBurst()
	if n <= 0 {
		return sourcePathProbeMaxBurst
	}
	return n
}

func (c *Conn) sourcePathReceiveFuncs() []conn.ReceiveFunc {
	if sourcePathAuxSocketCount() == 0 {
		return nil
	}

	c.sourcePath.mu.Lock()
	defer c.sourcePath.mu.Unlock()
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux6.setID(sourceIPv6SocketID)
	c.ensureSourcePathPConnLocked(&c.sourcePath.aux4.pconn)
	c.ensureSourcePathPConnLocked(&c.sourcePath.aux6.pconn)

	return []conn.ReceiveFunc{
		c.mkReceiveFuncWithSource(&c.sourcePath.aux4.pconn, nil, nil, nil, nil, nil, c.sourcePath.aux4.rxMeta),
		c.mkReceiveFuncWithSource(&c.sourcePath.aux6.pconn, nil, nil, nil, nil, nil, c.sourcePath.aux6.rxMeta),
	}
}

func (c *Conn) sourcePathProbeSources(is4 bool) []sourceRxMeta {
	if sourcePathAuxSocketCount() == 0 {
		return nil
	}
	c.sourcePath.mu.Lock()
	defer c.sourcePath.mu.Unlock()
	if is4 {
		if !c.sourcePath.aux4Bound {
			return nil
		}
		return []sourceRxMeta{c.sourcePath.aux4.rxMeta()}
	}
	if !c.sourcePath.aux6Bound {
		return nil
	}
	return []sourceRxMeta{c.sourcePath.aux6.rxMeta()}
}

func sourcePathForcedDataSourceMode() string {
	return strings.ToLower(envknobSrcSelForceDataSource())
}

func sourcePathForcedDataSourceAllowsAddr(addr netip.Addr) bool {
	return sourcePathForcedDataSourceModeAllowsAddr(sourcePathForcedDataSourceMode(), addr)
}

func sourcePathForcedDataSourceModeAllowsAddr(mode string, addr netip.Addr) bool {
	switch mode {
	case "aux":
		return true
	case "aux4", "ipv4", "v4":
		return addr.Is4()
	case "aux6", "ipv6", "v6":
		return addr.Is6()
	default:
		return false
	}
}

func (c *Conn) sourcePathDataSendSource(dst epAddr) sourceRxMeta {
	if sourcePathAuxSocketCount() == 0 || !dst.isDirect() {
		return primarySourceRxMeta
	}
	if forceMode := sourcePathForcedDataSourceMode(); forceMode != "" {
		if !sourcePathForcedDataSourceModeAllowsAddr(forceMode, dst.ap.Addr()) {
			return primarySourceRxMeta
		}
		return c.sourcePathForcedDataSendSource(dst)
	}
	if !envknobSrcSelAutoDataSource() {
		return primarySourceRxMeta
	}
	score, ok := c.sourcePathBestCandidate(dst)
	if !ok {
		return primarySourceRxMeta
	}
	return score.source
}

func (c *Conn) sourcePathForcedDataSendSource(dst epAddr) sourceRxMeta {
	c.sourcePath.mu.Lock()
	defer c.sourcePath.mu.Unlock()
	switch {
	case dst.ap.Addr().Is4() && c.sourcePath.aux4Bound:
		return c.sourcePath.aux4.rxMeta()
	case dst.ap.Addr().Is6() && c.sourcePath.aux6Bound:
		return c.sourcePath.aux6.rxMeta()
	default:
		return primarySourceRxMeta
	}
}

func (c *Conn) sourcePathWriteWireGuardBatchTo(source sourceRxMeta, dst epAddr, buffs [][]byte, offset int) error {
	c.sourcePath.mu.Lock()
	var ruc *RebindingUDPConn
	switch {
	case dst.ap.Addr().Is4() && dst.isDirect() && c.sourcePath.aux4Bound && source == c.sourcePath.aux4.rxMeta():
		ruc = &c.sourcePath.aux4.pconn
	case dst.ap.Addr().Is6() && dst.isDirect() && c.sourcePath.aux6Bound && source == c.sourcePath.aux6.rxMeta():
		ruc = &c.sourcePath.aux6.pconn
	}
	c.sourcePath.mu.Unlock()
	if ruc == nil {
		return errSourcePathUnavailable
	}
	return ruc.WriteWireGuardBatchTo(buffs, dst, offset)
}

func (c *Conn) sourcePathWriteTo(source sourceRxMeta, dst netip.AddrPort, pkt []byte) (int, error) {
	c.sourcePath.mu.Lock()
	var ruc *RebindingUDPConn
	switch {
	case dst.Addr().Is4() && c.sourcePath.aux4Bound && source == c.sourcePath.aux4.rxMeta():
		ruc = &c.sourcePath.aux4.pconn
	case dst.Addr().Is6() && c.sourcePath.aux6Bound && source == c.sourcePath.aux6.rxMeta():
		ruc = &c.sourcePath.aux6.pconn
	}
	c.sourcePath.mu.Unlock()
	if ruc == nil {
		return 0, errSourcePathUnavailable
	}
	return ruc.WriteToUDPAddrPort(pkt, dst)
}

func (c *Conn) rebindSourcePathSockets() error {
	if sourcePathAuxSocketCount() == 0 {
		c.closeSourcePathSockets()
		c.mu.Lock()
		c.sourceProbes.clearLocked()
		c.mu.Unlock()
		return nil
	}

	c.sourcePath.mu.Lock()
	defer c.sourcePath.mu.Unlock()
	c.sourcePath.generation++
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux6.setID(sourceIPv6SocketID)
	c.sourcePath.aux6.generation.Store(uint64(c.sourcePath.generation))

	err4 := c.bindSourcePathSocketLocked(&c.sourcePath.aux4.pconn, "udp4")
	c.sourcePath.aux4Bound = err4 == nil
	if err4 != nil {
		c.setSourcePathBlockForeverLocked(&c.sourcePath.aux4.pconn)
	}

	err6 := c.bindSourcePathSocketLocked(&c.sourcePath.aux6.pconn, "udp6")
	c.sourcePath.aux6Bound = err6 == nil
	if err6 != nil {
		c.setSourcePathBlockForeverLocked(&c.sourcePath.aux6.pconn)
	}

	return sourcePathBindError(err4, err6)
}

func (c *Conn) bindSourcePathSocketLocked(ruc *RebindingUDPConn, network string) error {
	ruc.mu.Lock()
	defer ruc.mu.Unlock()
	if err := ruc.closeLocked(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, errNilPConn) {
		c.logf("magicsock: srcsel: auxiliary %s close failed: %v", network, err)
	}
	pconn, err := c.listenPacket(network, 0)
	if err != nil {
		return err
	}
	trySetUDPSocketOptions(pconn, c.logf)
	ruc.setConnLocked(pconn, network, c.bind.BatchSize())
	return nil
}

func (c *Conn) ensureSourcePathPConnLocked(ruc *RebindingUDPConn) {
	ruc.mu.Lock()
	defer ruc.mu.Unlock()
	if ruc.pconn != nil {
		return
	}
	ruc.setConnLocked(newBlockForeverConn(), "", c.bind.BatchSize())
}

func (c *Conn) setSourcePathBlockForeverLocked(ruc *RebindingUDPConn) {
	ruc.mu.Lock()
	defer ruc.mu.Unlock()
	if err := ruc.closeLocked(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, errNilPConn) {
		c.logf("magicsock: srcsel: auxiliary close failed: %v", err)
	}
	ruc.setConnLocked(newBlockForeverConn(), "", c.bind.BatchSize())
}

func (c *Conn) closeSourcePathSockets() {
	c.sourcePath.mu.Lock()
	defer c.sourcePath.mu.Unlock()
	c.sourcePath.aux4Bound = false
	c.sourcePath.aux6Bound = false
	c.closeSourcePathPConnLocked(&c.sourcePath.aux4.pconn)
	c.closeSourcePathPConnLocked(&c.sourcePath.aux6.pconn)
}

func (c *Conn) closeSourcePathPConnLocked(ruc *RebindingUDPConn) {
	if err := ruc.Close(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, errNilPConn) {
		c.logf("magicsock: srcsel: auxiliary close failed: %v", err)
	}
}
