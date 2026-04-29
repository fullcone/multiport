// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"errors"
	"io"
	"net/netip"
	"sync/atomic"
	"time"

	"tailscale.com/disco"
	"tailscale.com/net/stun"
	"tailscale.com/syncs"
	"tailscale.com/tstime/mono"
	"tailscale.com/types/key"
)

const (
	sourcePathProbeHistoryLimit = 32
	sourcePathProbeMaxPeers     = 32
	sourcePathProbeMaxBurst     = 1
)

var (
	errSourcePathUnavailable              = errors.New("magicsock: source path unavailable")
	errSourcePathProbePeerBudgetExceeded  = errors.New("magicsock: source path probe peer budget exceeded")
	errSourcePathProbeBurstBudgetExceeded = errors.New("magicsock: source path probe burst budget exceeded")
)

type sourcePathState struct {
	mu         syncs.Mutex
	generation sourceGeneration
	aux4       sourcePathSocket
	aux6       sourcePathSocket
	aux4Bound  bool
	aux6Bound  bool
}

type sourcePathSocket struct {
	id         atomic.Uint32
	generation atomic.Uint64
	pconn      RebindingUDPConn
}

func (s *sourcePathSocket) setID(id SourceSocketID) {
	s.id.Store(uint32(id))
}

func (s *sourcePathSocket) rxMeta() sourceRxMeta {
	return sourceRxMeta{
		socketID:   SourceSocketID(s.id.Load()),
		generation: sourceGeneration(s.generation.Load()),
	}
}

func (m sourceRxMeta) isPrimary() bool {
	return m.socketID == primarySourceSocketID
}

type sourcePathProbeManager struct {
	pending map[stun.TxID]sourcePathProbeTx
	samples []sourcePathProbeSample
}

type sourcePathProbeTx struct {
	txid     stun.TxID
	dst      epAddr
	dstDisco key.DiscoPublic
	source   sourceRxMeta
	at       mono.Time
	size     int
}

type sourcePathProbeSample struct {
	txid     stun.TxID
	dst      epAddr
	pongFrom epAddr
	pongSrc  netip.AddrPort
	source   sourceRxMeta
	latency  time.Duration
	at       mono.Time
}

type sourcePathCandidateScore struct {
	source  sourceRxMeta
	latency time.Duration
	samples int
	lastAt  mono.Time
}

type sourcePathProbeAddResult uint8

const (
	sourcePathProbeAdded sourcePathProbeAddResult = iota
	sourcePathProbePeerBudgetExceeded
	sourcePathProbeBurstBudgetExceeded
)

func (pm *sourcePathProbeManager) addLocked(tx sourcePathProbeTx) sourcePathProbeAddResult {
	return pm.addWithBudgetLocked(tx, sourcePathProbeMaxPeerCount(), sourcePathProbeMaxBurstCount())
}

func (pm *sourcePathProbeManager) addWithBudgetLocked(tx sourcePathProbeTx, maxPeers, maxBurst int) sourcePathProbeAddResult {
	if pm.pending == nil {
		pm.pending = make(map[stun.TxID]sourcePathProbeTx)
	}
	pm.pruneExpiredLocked(tx.at)
	if maxPeers <= 0 {
		maxPeers = sourcePathProbeMaxPeers
	}
	if maxBurst <= 0 {
		maxBurst = sourcePathProbeMaxBurst
	}

	var (
		peerSeen    bool
		peerPending int
		peerCount   int
		seenPeers   = make(map[key.DiscoPublic]struct{})
	)
	for _, pending := range pm.pending {
		if pending.dstDisco == tx.dstDisco {
			peerSeen = true
			peerPending++
		}
		if _, ok := seenPeers[pending.dstDisco]; ok {
			continue
		}
		seenPeers[pending.dstDisco] = struct{}{}
		peerCount++
	}
	if !peerSeen && peerCount >= maxPeers {
		metricSourcePathProbePeerBudgetDropped.Add(1)
		return sourcePathProbePeerBudgetExceeded
	}
	if peerPending >= maxBurst {
		metricSourcePathProbeBurstBudgetDropped.Add(1)
		return sourcePathProbeBurstBudgetExceeded
	}
	pm.pending[tx.txid] = tx
	return sourcePathProbeAdded
}

func (pm *sourcePathProbeManager) forgetLocked(txid stun.TxID) {
	delete(pm.pending, txid)
}

func (pm *sourcePathProbeManager) pendingLenLocked() int {
	return len(pm.pending)
}

func (pm *sourcePathProbeManager) samplesLenLocked() int {
	return len(pm.samples)
}

func (c *Conn) sourcePathBestCandidate(dst epAddr) (sourcePathCandidateScore, bool) {
	if !dst.isDirect() {
		return sourcePathCandidateScore{}, false
	}

	sources := c.sourcePathProbeSources(dst.ap.Addr().Is4())
	if len(sources) == 0 {
		return sourcePathCandidateScore{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sourceProbes.bestCandidateLocked(dst, sources)
}

func (pm *sourcePathProbeManager) bestCandidateLocked(dst epAddr, sources []sourceRxMeta) (sourcePathCandidateScore, bool) {
	if !dst.isDirect() {
		return sourcePathCandidateScore{}, false
	}

	var best sourcePathCandidateScore
	var bestOK bool
	for _, source := range sources {
		if source.isPrimary() {
			continue
		}

		var candidate sourcePathCandidateScore
		var candidateOK bool
		for _, sample := range pm.samples {
			if sample.dst != dst || sample.source != source {
				continue
			}
			if !candidateOK {
				candidate = sourcePathCandidateScore{
					source:  source,
					latency: sample.latency,
					samples: 1,
					lastAt:  sample.at,
				}
				candidateOK = true
				continue
			}
			candidate.samples++
			if sample.latency < candidate.latency {
				candidate.latency = sample.latency
			}
			if sample.at.Sub(candidate.lastAt) > 0 {
				candidate.lastAt = sample.at
			}
		}
		if !candidateOK {
			continue
		}
		if !bestOK || candidate.latency < best.latency || (candidate.latency == best.latency && candidate.lastAt.Sub(best.lastAt) > 0) {
			best = candidate
			bestOK = true
		}
	}
	return best, bestOK
}

func (pm *sourcePathProbeManager) pruneExpiredLocked(now mono.Time) {
	var expired int64
	for txid, tx := range pm.pending {
		if now.Sub(tx.at) >= pingTimeoutDuration {
			delete(pm.pending, txid)
			expired++
		}
	}
	if expired > 0 {
		metricSourcePathProbePendingExpired.Add(expired)
	}
}

func (pm *sourcePathProbeManager) handlePongLocked(pong *disco.Pong, sender key.DiscoPublic, src epAddr, rxMeta sourceRxMeta) bool {
	if rxMeta.isPrimary() {
		return false
	}
	txid := stun.TxID(pong.TxID)
	tx, ok := pm.pending[txid]
	if !ok {
		return false
	}
	now := mono.Now()
	if now.Sub(tx.at) >= pingTimeoutDuration {
		delete(pm.pending, txid)
		metricSourcePathProbePongExpired.Add(1)
		return true
	}
	if tx.source != rxMeta || tx.dstDisco != sender {
		return false
	}
	delete(pm.pending, txid)

	pm.samples = append(pm.samples, sourcePathProbeSample{
		txid:     txid,
		dst:      tx.dst,
		pongFrom: src,
		pongSrc:  pong.Src,
		source:   rxMeta,
		latency:  now.Sub(tx.at),
		at:       now,
	})
	if len(pm.samples) > sourcePathProbeHistoryLimit {
		copy(pm.samples, pm.samples[len(pm.samples)-sourcePathProbeHistoryLimit:])
		pm.samples = pm.samples[:sourcePathProbeHistoryLimit]
	}
	metricSourcePathProbePongAccepted.Add(1)
	return true
}

func (c *Conn) sendSourcePathDiscoPing(source sourceRxMeta, dst epAddr, dstKey key.NodePublic, dstDisco key.DiscoPublic, txid stun.TxID, size int, logLevel discoLogLevel) (sent bool, err error) {
	if source.isPrimary() || !dst.isDirect() {
		return false, errSourcePathUnavailable
	}

	size = min(size, MaxDiscoPingSize)
	padding := max(size-discoPingSize, 0)
	msg := &disco.SourcePathProbe{
		TxID:    [12]byte(txid),
		NodeKey: c.publicKeyAtomic.Load(),
		Padding: padding,
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false, errConnClosed
	}
	if !c.peerMap.knownPeerDiscoKey(dstDisco) {
		c.mu.Unlock()
		return false, errors.New("unknown peer")
	}
	di := c.discoInfoForKnownPeerLocked(dstDisco)
	addResult := c.sourceProbes.addLocked(sourcePathProbeTx{
		txid:     txid,
		dst:      dst,
		dstDisco: dstDisco,
		source:   source,
		at:       mono.Now(),
		size:     size,
	})
	switch addResult {
	case sourcePathProbeAdded:
	case sourcePathProbePeerBudgetExceeded:
		c.mu.Unlock()
		return false, errSourcePathProbePeerBudgetExceeded
	case sourcePathProbeBurstBudgetExceeded:
		c.mu.Unlock()
		return false, errSourcePathProbeBurstBudgetExceeded
	default:
		c.mu.Unlock()
		return false, errSourcePathUnavailable
	}
	c.mu.Unlock()

	pkt := make([]byte, 0, 512)
	pkt = append(pkt, disco.Magic...)
	pkt = c.discoAtomic.Public().AppendTo(pkt)
	pkt = append(pkt, di.sharedKey.Seal(msg.AppendMarshal(nil))...)

	metricSendDiscoUDP.Add(1)
	n, err := c.sourcePathWriteTo(source, dst.ap, pkt)
	if err == nil && n != len(pkt) {
		err = io.ErrShortWrite
	}
	if err != nil {
		c.mu.Lock()
		c.sourceProbes.forgetLocked(txid)
		c.mu.Unlock()
		if !c.networkDown() && pmtuShouldLogDiscoTxErr(msg, err) {
			c.logf("magicsock: disco: failed to send source-path %v to %v %s: %v", disco.MessageSummary(msg), dst, dstKey.ShortString(), err)
		}
		return false, err
	}

	if logLevel == discoLog || (logLevel == discoVerboseLog && debugDisco()) {
		node := "?"
		if !dstKey.IsZero() {
			node = dstKey.ShortString()
		}
		c.dlogf("[v1] magicsock: disco: %v->%v (%v, %v, source=%d) sent %v len %v\n", c.discoAtomic.Short(), dstDisco.ShortString(), node, derpStr(dst.String()), source.socketID, disco.MessageSummary(msg), len(pkt))
	}
	metricSentDiscoUDP.Add(1)
	metricSentDiscoSourcePathProbe.Add(1)
	if size != 0 {
		metricSentDiscoPeerMTUProbes.Add(1)
		metricSentDiscoPeerMTUProbeBytes.Add(int64(pingSizeToPktLen(size, dst)))
	}
	return true, nil
}

func sourcePathBindError(errs ...error) error {
	return errors.Join(errs...)
}
