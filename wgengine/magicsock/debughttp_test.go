// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"fmt"
	"strings"
	"testing"

	"tailscale.com/net/stun"
)

func TestPrintSourcePathDebugHTML(t *testing.T) {
	var c Conn
	c.sourcePath.generation = 7
	c.sourcePath.aux4.setID(sourceIPv4SocketID)
	c.sourcePath.aux4.generation.Store(uint64(c.sourcePath.generation))
	c.sourcePath.aux4.pconn.mu.Lock()
	c.sourcePath.aux4.pconn.setConnLocked(newBlockForeverConn(), "", 0)
	c.sourcePath.aux4.pconn.mu.Unlock()
	c.sourcePath.aux4Bound = true
	c.sourcePath.aux6.setID(sourceIPv6SocketID)
	c.sourcePath.aux6.generation.Store(uint64(c.sourcePath.generation))
	c.sourceProbes.pending = map[stun.TxID]sourcePathProbeTx{
		stun.TxID{1}: {txid: stun.TxID{1}},
	}
	c.sourceProbes.samples = []sourcePathProbeSample{
		{txid: stun.TxID{2}, source: sourceRxMeta{socketID: sourceIPv4SocketID, generation: 7}},
	}

	var b strings.Builder
	c.printSourcePathDebugHTML(&b)

	body := b.String()
	for _, want := range []string{
		`<h2 id=srcsel>`,
		`generation: 7`,
		`pending probes: 1`,
		`samples: 1`,
		fmt.Sprintf("probe peer budget: %d", sourcePathProbeMaxPeerCount()),
		fmt.Sprintf("probe burst budget: %d", sourcePathProbeMaxBurstCount()),
		`auxiliary IPv4: bound, socketID aux4, generation 7`,
		`auxiliary IPv6: not bound, socketID aux6, generation 7`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("source selection debug HTML missing %q:\n%s", want, body)
		}
	}
}
