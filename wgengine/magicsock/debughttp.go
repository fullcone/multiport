// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"tailscale.com/feature"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/tailcfg"
	"tailscale.com/tstime/mono"
	"tailscale.com/types/key"
)

// ServeHTTPDebug serves an HTML representation of the innards of c for debugging.
//
// It's accessible either from tailscaled's debug port (at
// /debug/magicsock) or via peerapi to a peer that's owned by the same
// user (so they can e.g. inspect their phones).
func (c *Conn) ServeHTTPDebug(w http.ResponseWriter, r *http.Request) {
	if !buildfeatures.HasDebug {
		http.Error(w, feature.ErrUnavailable.Error(), http.StatusNotImplemented)
		return
	}

	now := time.Now()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<h1>magicsock</h1>")

	c.printSourcePathDebugHTML(w)

	c.mu.Lock()
	defer c.mu.Unlock()

	fmt.Fprintf(w, "<h2 id=derp><a href=#derp>#</a> DERP</h2><ul>")
	if c.derpMap != nil {
		type D struct {
			regionID   int
			lastWrite  time.Time
			createTime time.Time
		}
		ent := make([]D, 0, len(c.activeDerp))
		for rid, ad := range c.activeDerp {
			ent = append(ent, D{
				regionID:   rid,
				lastWrite:  *ad.lastWrite,
				createTime: ad.createTime,
			})
		}
		sort.Slice(ent, func(i, j int) bool {
			return ent[i].regionID < ent[j].regionID
		})
		for _, e := range ent {
			r, ok := c.derpMap.Regions[e.regionID]
			if !ok {
				continue
			}
			home := ""
			if e.regionID == c.myDerp {
				home = "🏠"
			}
			fmt.Fprintf(w, "<li>%s %d - %v: created %v ago, write %v ago</li>\n",
				home, e.regionID, html.EscapeString(r.RegionCode),
				now.Sub(e.createTime).Round(time.Second),
				now.Sub(e.lastWrite).Round(time.Second),
			)
		}

	}
	fmt.Fprintf(w, "</ul>\n")

	fmt.Fprintf(w, "<h2 id=ipport><a href=#ipport>#</a> ip:port to endpoint</h2><ul>")
	{
		type kv struct {
			addr epAddr
			pi   *peerInfo
		}
		ent := make([]kv, 0, len(c.peerMap.byEpAddr))
		for k, v := range c.peerMap.byEpAddr {
			ent = append(ent, kv{k, v})
		}
		sort.Slice(ent, func(i, j int) bool { return epAddrLess(ent[i].addr, ent[j].addr) })
		for _, e := range ent {
			ep := e.pi.ep
			shortStr := ep.publicKey.ShortString()
			fmt.Fprintf(w, "<li>%v: <a href='#%v'>%v</a></li>\n", e.addr, strings.Trim(shortStr, "[]"), shortStr)
		}

	}
	fmt.Fprintf(w, "</ul>\n")

	fmt.Fprintf(w, "<h2 id=bykey><a href=#bykey>#</a> endpoints by key</h2>")
	{
		type kv struct {
			pub key.NodePublic
			pi  *peerInfo
		}
		ent := make([]kv, 0, len(c.peerMap.byNodeKey))
		for k, v := range c.peerMap.byNodeKey {
			ent = append(ent, kv{k, v})
		}
		sort.Slice(ent, func(i, j int) bool { return ent[i].pub.Less(ent[j].pub) })

		peers := make(map[key.NodePublic]tailcfg.NodeView, len(c.peersByID))
		for _, p := range c.peersByID {
			peers[p.Key()] = p
		}

		for _, e := range ent {
			ep := e.pi.ep
			shortStr := e.pub.ShortString()
			name := peerDebugName(peers[e.pub])
			fmt.Fprintf(w, "<h3 id=%v><a href='#%v'>%v</a> - %s</h3>\n",
				strings.Trim(shortStr, "[]"),
				strings.Trim(shortStr, "[]"),
				shortStr,
				html.EscapeString(name))
			printEndpointHTML(w, ep)
		}

	}
}

func printEndpointHTML(w io.Writer, ep *endpoint) {
	lastRecv := ep.lastRecvWG.LoadAtomic()

	ep.mu.Lock()
	defer ep.mu.Unlock()
	if ep.lastSendExt == 0 && lastRecv == 0 {
		return // no activity ever
	}

	now := time.Now()
	mnow := mono.Now()
	fmtMono := func(m mono.Time) string {
		if m == 0 {
			return "-"
		}
		return mnow.Sub(m).Round(time.Millisecond).String()
	}

	fmt.Fprintf(w, "<p>Best: <b>%+v</b>, %v ago (for %v)</p>\n", ep.bestAddr, fmtMono(ep.bestAddrAt), ep.trustBestAddrUntil.Sub(mnow).Round(time.Millisecond))
	fmt.Fprintf(w, "<p>heartbeating: %v</p>\n", ep.heartBeatTimer != nil)
	fmt.Fprintf(w, "<p>lastSend: %v ago</p>\n", fmtMono(ep.lastSendExt))
	fmt.Fprintf(w, "<p>lastFullPing: %v ago</p>\n", fmtMono(ep.lastFullPing))

	eps := make([]netip.AddrPort, 0, len(ep.endpointState))
	for ipp := range ep.endpointState {
		eps = append(eps, ipp)
	}
	sort.Slice(eps, func(i, j int) bool { return addrPortLess(eps[i], eps[j]) })
	io.WriteString(w, "<p>Endpoints:</p><ul>")
	for _, ipp := range eps {
		s := ep.endpointState[ipp]
		if ipp == ep.bestAddr.ap && !ep.bestAddr.vni.IsSet() {
			fmt.Fprintf(w, "<li><b>%s</b>: (best)<ul>", ipp)
		} else {
			fmt.Fprintf(w, "<li>%s: ...<ul>", ipp)
		}
		fmt.Fprintf(w, "<li>lastPing: %v ago</li>\n", fmtMono(s.lastPing))
		if s.lastGotPing.IsZero() {
			fmt.Fprintf(w, "<li>disco-learned-at: -</li>\n")
		} else {
			fmt.Fprintf(w, "<li>disco-learned-at: %v ago</li>\n", now.Sub(s.lastGotPing).Round(time.Second))
		}
		fmt.Fprintf(w, "<li>callMeMaybeTime: %v</li>\n", s.callMeMaybeTime)
		for i := range s.recentPongs {
			if i == 5 {
				break
			}
			pos := (int(s.recentPong) - i) % len(s.recentPongs)
			// If s.recentPongs wraps around pos will be negative, so start
			// again from the end of the slice.
			if pos < 0 {
				pos += len(s.recentPongs)
			}
			pr := s.recentPongs[pos]
			fmt.Fprintf(w, "<li>pong %v ago: in %v, from %v src %v</li>\n",
				fmtMono(pr.pongAt), pr.latency.Round(time.Millisecond/10),
				pr.from, pr.pongSrc)
		}
		fmt.Fprintf(w, "</ul></li>\n")
	}
	io.WriteString(w, "</ul>")

}

type sourcePathDebugSnapshot struct {
	generation     sourceGeneration
	auxSocketCount int
	maxProbePeers  int
	maxProbeBurst  int
	pendingProbes  int
	samples        int
	aux4           sourcePathSocketDebugSnapshot
	aux6           sourcePathSocketDebugSnapshot
	extraAux       []sourcePathSocketDebugSnapshot
}

type sourcePathSocketDebugSnapshot struct {
	label      string
	bound      bool
	socketID   SourceSocketID
	generation sourceGeneration
	localAddr  string
}

// SourcePathStatus is a LocalAPI/debug snapshot of source-selection state.
// It is not a stable API.
type SourcePathStatus struct {
	Generation     uint64                 `json:"generation"`
	AuxSocketCount int                    `json:"aux_socket_count"`
	PendingProbes  int                    `json:"pending_probes"`
	Samples        int                    `json:"samples"`
	Peers          []SourcePathPeerStatus `json:"peers"`
}

// SourcePathPeerStatus is a per-peer source-selection debug snapshot.
type SourcePathPeerStatus struct {
	NodeID         tailcfg.NodeID         `json:"node_id"`
	NodeAddr       string                 `json:"node_addr,omitempty"`
	PublicKey      string                 `json:"public_key"`
	BestAddr       string                 `json:"best_addr,omitempty"`
	CandidateCount int                    `json:"candidate_count"`
	Active         []SourcePathPathStatus `json:"active"`
	Standby        []SourcePathPathStatus `json:"standby"`
}

// SourcePathPathStatus is one remote endpoint + local source socket path.
type SourcePathPathStatus struct {
	Remote        string  `json:"remote"`
	Source        string  `json:"source"`
	SourceID      uint32  `json:"source_id"`
	Generation    uint64  `json:"generation"`
	HasLatency    bool    `json:"has_latency"`
	LatencyMS     float64 `json:"latency_ms,omitempty"`
	LastSampleAgo string  `json:"last_sample_ago,omitempty"`
}

// SourcePathStatus returns source-selection active/standby path state. It is
// intentionally best-effort debug state and may omit peers with no path pool.
func (c *Conn) SourcePathStatus() SourcePathStatus {
	now := mono.Now()
	socketSnapshot := c.sourcePathDebugSnapshot()
	status := SourcePathStatus{
		Generation:     uint64(socketSnapshot.generation),
		AuxSocketCount: socketSnapshot.auxSocketCount,
		PendingProbes:  socketSnapshot.pendingProbes,
		Samples:        socketSnapshot.samples,
	}

	c.mu.Lock()
	endpoints := make([]*endpoint, 0, c.peerMap.nodeCount())
	c.peerMap.forEachEndpoint(func(ep *endpoint) {
		endpoints = append(endpoints, ep)
	})
	c.mu.Unlock()

	for _, ep := range endpoints {
		ep.mu.Lock()
		bestAddr := ep.bestAddr.epAddr
		candidates := ep.sourcePathDualSendDstCandidatesLocked(now, bestAddr)
		active := make([]sourcePathSendPath, 0, ep.sourcePathActiveCount)
		for i := 0; i < ep.sourcePathActiveCount && i < len(ep.sourcePathActivePaths); i++ {
			active = append(active, ep.sourcePathActivePaths[i])
		}
		peer := SourcePathPeerStatus{
			NodeID:         ep.nodeID,
			PublicKey:      ep.publicKey.ShortString(),
			CandidateCount: len(candidates),
		}
		if ep.nodeAddr.IsValid() {
			peer.NodeAddr = ep.nodeAddr.String()
		}
		if bestAddr.ap.IsValid() {
			peer.BestAddr = bestAddr.ap.String()
		}
		ep.mu.Unlock()

		ranked := c.sourcePathRankedDualSendPaths(candidates, now)
		peer.Active = sourcePathStatusPaths(active, now)
		peer.Standby = sourcePathStandbyStatusPaths(ranked, active, now)
		if len(peer.Active) == 0 && len(peer.Standby) == 0 && peer.CandidateCount == 0 {
			continue
		}
		status.Peers = append(status.Peers, peer)
	}
	return status
}

func sourcePathStandbyStatusPaths(ranked, active []sourcePathSendPath, now mono.Time) []SourcePathPathStatus {
	var standby []sourcePathSendPath
	for _, path := range ranked {
		if sourcePathSendPathIndex(active, path) >= 0 {
			continue
		}
		standby = append(standby, path)
	}
	return sourcePathStatusPaths(standby, now)
}

func sourcePathStatusPaths(paths []sourcePathSendPath, now mono.Time) []SourcePathPathStatus {
	out := make([]SourcePathPathStatus, 0, len(paths))
	for _, path := range paths {
		item := SourcePathPathStatus{
			Remote:     path.dst.ap.String(),
			Source:     sourceSocketIDDebugString(path.source.socketID),
			SourceID:   uint32(path.source.socketID),
			Generation: uint64(path.source.generation),
			HasLatency: path.hasLatency,
		}
		if path.hasLatency {
			item.LatencyMS = float64(path.latency) / float64(time.Millisecond)
		}
		if !path.lastAt.IsZero() {
			item.LastSampleAgo = now.Sub(path.lastAt).Round(time.Millisecond).String()
		}
		out = append(out, item)
	}
	return out
}

func (c *Conn) sourcePathDebugSnapshot() sourcePathDebugSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	s := sourcePathDebugSnapshot{
		auxSocketCount: sourcePathAuxSocketCount(),
		maxProbePeers:  sourcePathProbeMaxPeerCount(),
		maxProbeBurst:  sourcePathProbeMaxBurstCount(),
		pendingProbes:  c.sourceProbes.pendingLenLocked(),
		samples:        c.sourceProbes.samplesLenLocked(),
	}

	c.sourcePath.mu.Lock()
	defer c.sourcePath.mu.Unlock()
	s.generation = c.sourcePath.generation
	s.aux4 = sourcePathSocketDebugSnapshotLocked("auxiliary IPv4", &c.sourcePath.aux4, c.sourcePath.aux4Bound)
	s.aux6 = sourcePathSocketDebugSnapshotLocked("auxiliary IPv6", &c.sourcePath.aux6, c.sourcePath.aux6Bound)
	for i := range c.sourcePath.extraAux4 {
		label := fmt.Sprintf("auxiliary IPv4 #%d", i+2)
		s.extraAux = append(s.extraAux, sourcePathSocketDebugSnapshotLocked(label, &c.sourcePath.extraAux4[i], c.sourcePath.extra4Bound[i]))
	}
	for i := range c.sourcePath.extraAux6 {
		label := fmt.Sprintf("auxiliary IPv6 #%d", i+2)
		s.extraAux = append(s.extraAux, sourcePathSocketDebugSnapshotLocked(label, &c.sourcePath.extraAux6[i], c.sourcePath.extra6Bound[i]))
	}
	return s
}

func sourcePathSocketDebugSnapshotLocked(label string, s *sourcePathSocket, bound bool) sourcePathSocketDebugSnapshot {
	meta := s.rxMeta()
	return sourcePathSocketDebugSnapshot{
		label:      label,
		bound:      bound,
		socketID:   meta.socketID,
		generation: meta.generation,
		localAddr:  sourcePathDebugLocalAddr(&s.pconn),
	}
}

func sourcePathDebugLocalAddr(ruc *RebindingUDPConn) string {
	ruc.mu.Lock()
	defer ruc.mu.Unlock()
	if ruc.pconn == nil {
		return "-"
	}
	return ruc.localAddrLocked().String()
}

func (c *Conn) printSourcePathDebugHTML(w io.Writer) {
	s := c.sourcePathDebugSnapshot()
	fmt.Fprintf(w, "<h2 id=srcsel><a href=#srcsel>#</a> Source selection</h2><ul>")
	fmt.Fprintf(w, "<li>generation: %d</li>\n", s.generation)
	fmt.Fprintf(w, "<li>enabled auxiliary sockets: %d</li>\n", s.auxSocketCount)
	fmt.Fprintf(w, "<li>pending probes: %d</li>\n", s.pendingProbes)
	fmt.Fprintf(w, "<li>samples: %d</li>\n", s.samples)
	fmt.Fprintf(w, "<li>probe peer budget: %d</li>\n", s.maxProbePeers)
	fmt.Fprintf(w, "<li>probe burst budget: %d</li>\n", s.maxProbeBurst)
	sourcePathSocketDebugHTML(w, s.aux4)
	sourcePathSocketDebugHTML(w, s.aux6)
	for _, aux := range s.extraAux {
		sourcePathSocketDebugHTML(w, aux)
	}
	fmt.Fprintf(w, "</ul>\n")
}

func sourcePathSocketDebugHTML(w io.Writer, s sourcePathSocketDebugSnapshot) {
	state := "not bound"
	if s.bound {
		state = "bound"
	}
	fmt.Fprintf(w, "<li>%s: %s, socketID %s, generation %d, local %s</li>\n",
		html.EscapeString(s.label),
		state,
		html.EscapeString(sourceSocketIDDebugString(s.socketID)),
		s.generation,
		html.EscapeString(s.localAddr))
}

func sourceSocketIDDebugString(id SourceSocketID) string {
	switch id {
	case primarySourceSocketID:
		return "primary"
	case sourceIPv4SocketID:
		return "aux4"
	case sourceIPv6SocketID:
		return "aux6"
	default:
		if id >= sourceIPv4ExtraSocketIDBase && id < sourceIPv4ExtraSocketIDBase+SourceSocketID(sourcePathMaxAuxSockets) {
			return fmt.Sprintf("aux4.%d", id-sourceIPv4ExtraSocketIDBase+2)
		}
		if id >= sourceIPv6ExtraSocketIDBase && id < sourceIPv6ExtraSocketIDBase+SourceSocketID(sourcePathMaxAuxSockets) {
			return fmt.Sprintf("aux6.%d", id-sourceIPv6ExtraSocketIDBase+2)
		}
		return fmt.Sprintf("unknown(%d)", id)
	}
}

func peerDebugName(p tailcfg.NodeView) string {
	if !p.Valid() {
		return ""
	}
	n := p.Name()
	if base, _, ok := strings.Cut(n, "."); ok {
		return base
	}
	return p.Hostinfo().Hostname()
}

func addrPortLess(a, b netip.AddrPort) bool {
	if v := a.Addr().Compare(b.Addr()); v != 0 {
		return v < 0
	}
	return a.Port() < b.Port()
}

func epAddrLess(a, b epAddr) bool {
	if v := a.ap.Addr().Compare(b.ap.Addr()); v != 0 {
		return v < 0
	}
	if a.ap.Port() == b.ap.Port() {
		return a.vni.Get() < b.vni.Get()
	}
	return a.ap.Port() < b.ap.Port()
}
