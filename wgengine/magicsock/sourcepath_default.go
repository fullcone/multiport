// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !linux

package magicsock

import (
	"net/netip"

	"github.com/tailscale/wireguard-go/conn"
)

func (c *Conn) sourcePathReceiveFuncs() []conn.ReceiveFunc { return nil }

func sourcePathAuxSocketCount() int { return 0 }

func (c *Conn) sourcePathProbeSources(is4 bool) []sourceRxMeta { return nil }

func (c *Conn) sourcePathDataSendSource(dst epAddr) sourceRxMeta { return primarySourceRxMeta }

func sourcePathProbeMaxPeerCount() int { return sourcePathProbeMaxPeers }

func sourcePathProbeMaxBurstCount() int { return sourcePathProbeMaxBurst }

func (c *Conn) sourcePathWriteWireGuardBatchTo(source sourceRxMeta, dst epAddr, buffs [][]byte, offset int) error {
	return errSourcePathUnavailable
}

func (c *Conn) sourcePathWriteTo(source sourceRxMeta, dst netip.AddrPort, pkt []byte) (int, error) {
	return 0, errSourcePathUnavailable
}

func (c *Conn) rebindSourcePathSockets() error { return nil }

func (c *Conn) closeSourcePathSockets() {}
