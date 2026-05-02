// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"io"
	"math"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/tailscale/wireguard-go/device"
	"tailscale.com/disco"
	"tailscale.com/net/stun"
	"tailscale.com/syncs"
	"tailscale.com/tstime/mono"
	"tailscale.com/types/key"
)

const (
	// sourcePathProbeHistoryLimit bounds the global sample buffer. A FIFO
	// ring at this size meant a per-Conn ceiling regardless of peer count,
	// so with N peers each pair was only guaranteed limit/N samples on
	// average. Lift it to a memory-safety hard cap; freshness is enforced
	// by sourcePathSampleTTL during scoring and by pruneExpiredSamplesLocked
	// when new samples arrive. Override via TS_EXPERIMENTAL_SRCSEL_MAX_SAMPLES.
	sourcePathProbeHistoryLimit = 100000

	// sourcePathProbeMaxPeers is the default per-Conn cap on distinct peers
	// with pending probes. Zero means no policy limit — server-class
	// deployments may carry many peers and probe traffic itself is
	// negligible. A non-zero override can still be configured via
	// TS_EXPERIMENTAL_SRCSEL_MAX_PEERS.
	sourcePathProbeMaxPeers = 0

	// sourcePathProbeMaxBurst is the default cap on simultaneously pending
	// probes per peer. Eight allows IPv4 + IPv6 plus a few concurrent samples
	// to fill quickly; tests and tighter operational caps may override via
	// TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST.
	sourcePathProbeMaxBurst = 8

	// sourcePathProbeHardPendingCap bounds the total number of pending
	// probes across all peers. Unlike sourcePathProbeMaxPeers /
	// sourcePathProbeMaxBurst this is a memory-safety hard cap, not a
	// policy choice: it exists so a peer that never replies (for example
	// because it does not recognize disco.SourcePathProbe) cannot grow the
	// pending map without bound. Override only via
	// TS_EXPERIMENTAL_SRCSEL_MAX_PENDING.
	sourcePathProbeHardPendingCap = 100000

	// sourcePathProbeOutcomeLimit bounds the global probe outcome buffer
	// used for loss scoring. Freshness is still enforced by
	// sourcePathLossWindow, but the hard cap keeps high probe rates from
	// accumulating unbounded outcomes inside that window. Override via
	// TS_EXPERIMENTAL_SRCSEL_MAX_OUTCOMES.
	sourcePathProbeOutcomeLimit = 100000

	// sourcePathSampleTTL is the maximum age of a probe sample considered by
	// the scorer. Older samples are skipped so a stale lucky measurement does
	// not pin auxiliary selection on a path whose NAT mapping or routing has
	// since changed.
	sourcePathSampleTTL = 60 * time.Second

	// sourcePathRealtimeSampleTTL is the Phase 24 realtime profile freshness
	// window used with the dedicated 1 Hz probe cadence.
	sourcePathRealtimeSampleTTL = 10 * time.Second

	// sourcePathMinSamplesForUse is the minimum number of TTL-fresh samples a
	// (dst, source) pair must have before automatic source selection is
	// allowed to use it. The gate keeps a single lucky probe from steering
	// real WireGuard traffic.
	sourcePathMinSamplesForUse = 3

	// sourcePathAuxBeatThresholdPercent is the percentage by which an
	// auxiliary candidate's mean latency must beat the primary path's
	// observed latency before automatic selection is allowed to use it.
	// 10 means aux mean must be < primary RTT × 0.90. Override via
	// TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT (clamped to [0, 100]).
	// A value of 0 means any improvement is enough; 100 disables aux
	// selection entirely. The threshold is only applied when the caller
	// supplies a non-zero primary RTT — when primary latency is unknown
	// the scorer falls back to absolute aux selection (Phase 19 behavior).
	sourcePathAuxBeatThresholdPercent = 10

	// Phase 23 dual-send defaults.
	sourcePathDualSendAuxDropStreak = 5
	sourcePathDualSendRecovery      = 30 * time.Second
	sourcePathDualSendMaxSkew       = 100 * time.Millisecond

	// Phase 25 active-backup defaults.
	sourcePathActiveBackupPrimaryFailStreak = 3
	sourcePathActiveBackupFailoverHold      = 30 * time.Second
	sourcePathActiveBackupRecoveryPongs     = 3

	// Phase 24 multi-metric scorer defaults.
	sourcePathProbeIntervalFloor = 200 * time.Millisecond
	sourcePathRealtimeProbeEvery = time.Second
	sourcePathLossWindow         = 30 * time.Second
	sourcePathLatencyMax         = 300 * time.Millisecond
	sourcePathJitterMax          = 50 * time.Millisecond
	sourcePathLossMax            = 0.05
	sourcePathScoreImprovePct    = 5

	// Phase 26 flow-aware source selection defaults.
	sourcePathFlowIdle       = 30 * time.Second
	sourcePathFlowMaxEntries = 100000
)

var (
	errSourcePathUnavailable              = errors.New("magicsock: source path unavailable")
	errSourcePathProbePeerBudgetExceeded  = errors.New("magicsock: source path probe peer budget exceeded")
	errSourcePathProbeBurstBudgetExceeded = errors.New("magicsock: source path probe burst budget exceeded")
	errSourcePathProbeHardCapExceeded     = errors.New("magicsock: source path probe pending hard cap exceeded")
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
	pending                map[stun.TxID]sourcePathProbeTx
	samples                []sourcePathProbeSample
	outcomes               []sourcePathProbeOutcome
	activeBackup           map[epAddr]sourcePathActiveBackupState
	dualSendAuxFailStreak  map[sourcePathDualSendKey]int
	dualSendDemotedAuxTill map[sourcePathDualSendKey]mono.Time
	flowMap                map[sourcePathFlowKey]sourcePathFlowState
	flowRR                 map[epAddr]uint64
}

type sourcePathActiveBackupState struct {
	sendFailStreak int
	failoverAt     mono.Time
	holdUntil      mono.Time
	source         sourceRxMeta
}

type sourcePathDualSendKey struct {
	dst    epAddr
	source sourceRxMeta
}

type sourcePathFlowKey struct {
	dst epAddr
	id  uint64
}

type sourcePathFlowState struct {
	source       sourceRxMeta
	lastActivity mono.Time
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

type sourcePathProbeOutcome struct {
	dst    epAddr
	source sourceRxMeta
	at     mono.Time
	lost   bool
}

type sourcePathCandidateScore struct {
	source  sourceRxMeta
	latency time.Duration
	jitter  time.Duration
	loss    float64
	score   float64
	samples int
	lastAt  mono.Time
}

type sourcePathScoreWeights struct {
	latency float64
	jitter  float64
	loss    float64
}

type sourcePathProbeAddResult uint8

const (
	sourcePathProbeAdded sourcePathProbeAddResult = iota
	sourcePathProbePeerBudgetExceeded
	sourcePathProbeBurstBudgetExceeded
	sourcePathProbeHardCapExceeded
)

func (pm *sourcePathProbeManager) addLocked(tx sourcePathProbeTx) sourcePathProbeAddResult {
	return pm.addWithBudgetLocked(tx, sourcePathProbeMaxPeerCount(), sourcePathProbeMaxBurstCount(), sourcePathProbeHardPendingCount())
}

// addWithBudgetLocked attempts to track tx as a pending probe. maxPeers <= 0
// disables the per-peer-count policy cap (unlimited peers). maxBurst <= 0
// falls back to the default burst constant. hardCap is the memory-safety hard
// cap on total pending probes; <= 0 disables it (use only for tests).
func (pm *sourcePathProbeManager) addWithBudgetLocked(tx sourcePathProbeTx, maxPeers, maxBurst, hardCap int) sourcePathProbeAddResult {
	if pm.pending == nil {
		pm.pending = make(map[stun.TxID]sourcePathProbeTx)
	}
	pm.pruneExpiredLocked(tx.at)
	if maxBurst <= 0 {
		maxBurst = sourcePathProbeMaxBurst
	}

	if hardCap > 0 && len(pm.pending) >= hardCap {
		metricSourcePathProbeHardCapDropped.Add(1)
		return sourcePathProbeHardCapExceeded
	}

	var (
		peerSeen    bool
		peerPending int
	)
	if maxPeers > 0 {
		var (
			peerCount int
			seenPeers = make(map[key.DiscoPublic]struct{})
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
	} else {
		// Unlimited peers: only count this peer's pending burst.
		for _, pending := range pm.pending {
			if pending.dstDisco == tx.dstDisco {
				peerPending++
			}
		}
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

func (pm *sourcePathProbeManager) clearLocked() {
	clear(pm.pending)
	pm.samples = nil
	pm.outcomes = nil
	pm.activeBackup = nil
	pm.dualSendAuxFailStreak = nil
	pm.dualSendDemotedAuxTill = nil
	pm.flowMap = nil
	pm.flowRR = nil
}

// noteSourcePathSendFailure invalidates probe samples for (dst, source) after
// a real-data send through that source failed. This forces the scorer to wait
// for fresh probe evidence before steering data back to the failed pair.
// No-op for the primary source.
func (c *Conn) noteSourcePathSendFailure(dst epAddr, source sourceRxMeta) {
	if source.isPrimary() {
		return
	}
	c.mu.Lock()
	dropped := c.sourceProbes.invalidateLocked(dst, source)
	droppedFlows := c.sourceProbes.dropFlowsLocked(dst, source)
	c.mu.Unlock()
	if dropped > 0 {
		metricSourcePathSendFailureInvalidated.Add(int64(dropped))
	}
	if droppedFlows > 0 {
		metricSourcePathFlowEvictedSourceFailure.Add(int64(droppedFlows))
	}
}

func (c *Conn) sourcePathBestCandidate(dst epAddr) (sourcePathCandidateScore, bool) {
	if !dst.isDirect() {
		return sourcePathCandidateScore{}, false
	}

	sources := c.sourcePathProbeSources(dst.ap.Addr().Is4())
	if len(sources) == 0 {
		return sourcePathCandidateScore{}, false
	}

	primaryRTT := c.primaryRTTForDst(dst)

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sourceProbes.bestCandidateLocked(dst, sources, mono.Now(), primaryRTT)
}

func (c *Conn) sourcePathBestFailoverCandidate(dst epAddr, now mono.Time) (sourceRxMeta, bool) {
	if !dst.isDirect() {
		return sourceRxMeta{}, false
	}
	sources := c.sourcePathProbeSources(dst.ap.Addr().Is4())
	if len(sources) == 0 {
		return sourceRxMeta{}, false
	}
	c.mu.Lock()
	score, ok := c.sourceProbes.bestCandidateLocked(dst, sources, now, 0)
	c.mu.Unlock()
	if !ok {
		return sourceRxMeta{}, false
	}
	return score.source, true
}

func (c *Conn) noteSourcePathPrimarySendSuccess(dst epAddr) {
	if !sourcePathActiveBackupEnabled() || !dst.isDirect() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.sourceProbes.activeBackup[dst]
	if !ok {
		return
	}
	st.sendFailStreak = 0
	c.sourceProbes.activeBackup[dst] = st
}

func (c *Conn) noteSourcePathPrimarySendFailure(dst epAddr, now mono.Time) (sourceRxMeta, bool) {
	if !sourcePathActiveBackupEnabled() || !dst.isDirect() || sourcePathAuxSocketCount() == 0 {
		return sourceRxMeta{}, false
	}

	c.mu.Lock()
	if c.sourceProbes.activeBackup == nil {
		c.sourceProbes.activeBackup = make(map[epAddr]sourcePathActiveBackupState)
	}
	st := c.sourceProbes.activeBackup[dst]
	st.sendFailStreak++
	if !st.failoverAt.IsZero() && !st.source.isPrimary() {
		c.sourceProbes.activeBackup[dst] = st
		source := st.source
		c.mu.Unlock()
		return source, true
	}
	if st.sendFailStreak < sourcePathActiveBackupPrimaryFailStreakValue() {
		c.sourceProbes.activeBackup[dst] = st
		c.mu.Unlock()
		return sourceRxMeta{}, false
	}
	c.mu.Unlock()

	source, ok := c.sourcePathBestFailoverCandidate(dst, now)
	if !ok {
		return sourceRxMeta{}, false
	}

	c.mu.Lock()
	st = c.sourceProbes.activeBackup[dst]
	st.failoverAt = now
	st.holdUntil = now.Add(sourcePathActiveBackupFailoverHoldValue())
	st.source = source
	c.sourceProbes.activeBackup[dst] = st
	c.mu.Unlock()

	metricSourcePathPrimaryUnhealthySendStreak.Add(1)
	metricSourcePathFailoverToAux.Add(1)
	return source, true
}

func (c *Conn) sourcePathActiveBackupCandidate(dst epAddr, now mono.Time) (sourceRxMeta, bool) {
	if !sourcePathActiveBackupEnabled() || !dst.isDirect() {
		return sourceRxMeta{}, false
	}

	c.mu.Lock()
	st, ok := c.sourceProbes.activeBackup[dst]
	c.mu.Unlock()
	if !ok || st.failoverAt.IsZero() || st.source.isPrimary() {
		return sourceRxMeta{}, false
	}

	pongs := c.primaryPongsSinceDst(dst, st.failoverAt)
	recoveryPongs := sourcePathActiveBackupRecoveryPongsValue()
	if pongs >= recoveryPongs || (now.After(st.holdUntil) && pongs > 0) {
		c.mu.Lock()
		if current, ok := c.sourceProbes.activeBackup[dst]; ok && current.failoverAt == st.failoverAt {
			delete(c.sourceProbes.activeBackup, dst)
			metricSourcePathFailoverRecoveredToPrimary.Add(1)
		}
		c.mu.Unlock()
		return sourceRxMeta{}, false
	}
	if c.sourcePathSourceAvailable(dst, st.source) {
		return st.source, true
	}
	source, ok := c.sourcePathBestFailoverCandidate(dst, now)
	if ok {
		c.mu.Lock()
		if current, ok := c.sourceProbes.activeBackup[dst]; ok && current.failoverAt == st.failoverAt {
			current.source = source
			c.sourceProbes.activeBackup[dst] = current
		}
		c.mu.Unlock()
		return source, true
	}
	c.mu.Lock()
	if current, ok := c.sourceProbes.activeBackup[dst]; ok && current.failoverAt == st.failoverAt {
		delete(c.sourceProbes.activeBackup, dst)
	}
	c.mu.Unlock()
	return sourceRxMeta{}, false
}

func (c *Conn) sourcePathSourceAvailable(dst epAddr, source sourceRxMeta) bool {
	if source.isPrimary() || !dst.isDirect() {
		return source.isPrimary()
	}
	for _, current := range c.sourcePathProbeSources(dst.ap.Addr().Is4()) {
		if current == source {
			return true
		}
	}
	return false
}

func (c *Conn) primaryPongsSinceDst(dst epAddr, since mono.Time) int {
	c.mu.Lock()
	de, ok := c.peerMap.endpointForEpAddr(dst)
	c.mu.Unlock()
	if !ok || de == nil {
		return 0
	}
	de.mu.Lock()
	defer de.mu.Unlock()
	return de.primaryPongsSinceLocked(dst, since)
}

func (c *Conn) sourcePathDualSendCandidate(dst epAddr) (sourceRxMeta, bool) {
	if !sourcePathDualSendEnabled() || !dst.isDirect() {
		return sourceRxMeta{}, false
	}

	score, ok := c.sourcePathBestCandidate(dst)
	if !ok {
		return sourceRxMeta{}, false
	}

	if maxSkew := sourcePathDualSendMaxSkewValue(); maxSkew > 0 {
		if primaryRTT := c.primaryRTTForDst(dst); primaryRTT > 0 && absDuration(score.latency-primaryRTT) >= maxSkew {
			metricSourcePathDualSendSkippedSkew.Add(1)
			return sourceRxMeta{}, false
		}
	}

	key := sourcePathDualSendKey{dst: dst, source: score.source}
	now := mono.Now()
	c.mu.Lock()
	demoted := c.sourceProbes.dualSendDemotedLocked(key, now)
	c.mu.Unlock()
	if demoted {
		return sourceRxMeta{}, false
	}
	return score.source, true
}

func (c *Conn) startSourcePathProbeLoop() {
	interval := sourcePathProbeIntervalValue()
	if interval <= 0 || sourcePathAuxSocketCount() == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.sendSourcePathProbeTick()
			case <-c.donec:
				return
			}
		}
	}()
}

func (c *Conn) sendSourcePathProbeTick() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	var endpoints []*endpoint
	c.peerMap.forEachEndpoint(func(ep *endpoint) {
		endpoints = append(endpoints, ep)
	})
	c.mu.Unlock()

	for _, de := range endpoints {
		de.mu.Lock()
		epDisco := de.disco.Load()
		if epDisco == nil {
			de.mu.Unlock()
			continue
		}
		var dsts []epAddr
		if de.bestAddr.epAddr.isDirect() {
			dsts = append(dsts, de.bestAddr.epAddr)
		} else {
			for ap := range de.endpointState {
				dst := epAddr{ap: ap}
				if dst.isDirect() {
					dsts = append(dsts, dst)
				}
			}
		}
		dstKey := de.publicKey
		dstDisco := epDisco.key
		de.mu.Unlock()

		for _, dst := range dsts {
			for _, source := range c.sourcePathProbeSources(dst.ap.Addr().Is4()) {
				go c.sendSourcePathDiscoPing(source, dst, dstKey, dstDisco, stun.NewTxID(), 0, discoVerboseLog)
			}
		}
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// primaryRTTForDst looks up the endpoint that owns dst and returns its most
// recent observed primary-path RTT, or 0 if the endpoint, the dst's per-address
// pong history, and bestAddr all lack a usable measurement. The lookup
// acquires c.mu and de.mu in the conventional order and releases both before
// returning, so callers may freely re-enter c.mu afterward.
func (c *Conn) primaryRTTForDst(dst epAddr) time.Duration {
	c.mu.Lock()
	de, ok := c.peerMap.endpointForEpAddr(dst)
	c.mu.Unlock()
	if !ok || de == nil {
		return 0
	}
	de.mu.Lock()
	defer de.mu.Unlock()
	return de.primaryRTTForLocked(dst)
}

func (pm *sourcePathProbeManager) bestCandidateLocked(dst epAddr, sources []sourceRxMeta, now mono.Time, primaryRTT time.Duration) (sourcePathCandidateScore, bool) {
	if !dst.isDirect() {
		return sourcePathCandidateScore{}, false
	}

	thresholdPct := sourcePathAuxBeatThresholdPercentValue()
	multiMetric := sourcePathMultiMetricEnabled()
	var best sourcePathCandidateScore
	var bestOK bool
	for _, source := range sources {
		if source.isPrimary() {
			continue
		}

		var (
			candidate    sourcePathCandidateScore
			candidateOK  bool
			sumLatencyNs float64
			sumSqNs      float64
		)
		for _, sample := range pm.samples {
			if sample.dst != dst || sample.source != source {
				continue
			}
			if now.Sub(sample.at) > sourcePathSampleTTLValue() {
				continue
			}
			latencyNs := float64(sample.latency.Nanoseconds())
			sumLatencyNs += latencyNs
			sumSqNs += latencyNs * latencyNs
			if !candidateOK {
				candidate = sourcePathCandidateScore{
					source:  source,
					samples: 1,
					lastAt:  sample.at,
				}
				candidateOK = true
				continue
			}
			candidate.samples++
			if sample.at.Sub(candidate.lastAt) > 0 {
				candidate.lastAt = sample.at
			}
		}
		if !candidateOK || candidate.samples < sourcePathMinSamplesForUse {
			continue
		}
		// Mean latency over TTL-fresh samples. Mean is more representative of
		// what users will see for real data than the historical minimum, and
		// it dampens the influence of any single lucky packet.
		meanNs := sumLatencyNs / float64(candidate.samples)
		candidate.latency = time.Duration(meanNs)
		varianceNs := sumSqNs/float64(candidate.samples) - meanNs*meanNs
		if varianceNs < 0 {
			varianceNs = 0
		}
		candidate.jitter = time.Duration(math.Sqrt(varianceNs))
		candidate.loss = pm.lossRatioLocked(dst, source, now)

		if multiMetric {
			if !candidate.applyMultiMetricScore(primaryRTT) {
				continue
			}
		} else if primaryRTT > 0 && thresholdPct > 0 {
			// aux mean must be strictly less than primaryRTT × (1 - threshold).
			// Convert the percent threshold into an integer Duration cutoff to
			// avoid float64 rounding noise on small RTTs.
			cutoff := primaryRTT - primaryRTT*time.Duration(thresholdPct)/100
			if candidate.latency >= cutoff {
				metricSourcePathPrimaryBeatRejected.Add(1)
				continue
			}
		}
		if !bestOK || candidate.betterThan(best, multiMetric) {
			best = candidate
			bestOK = true
		}
	}
	return best, bestOK
}

func (c sourcePathCandidateScore) betterThan(best sourcePathCandidateScore, multiMetric bool) bool {
	if multiMetric {
		return c.score > best.score || (c.score == best.score && c.lastAt.Sub(best.lastAt) > 0)
	}
	return c.latency < best.latency || (c.latency == best.latency && c.lastAt.Sub(best.lastAt) > 0)
}

func (c *sourcePathCandidateScore) applyMultiMetricScore(primaryRTT time.Duration) bool {
	if c.latency > sourcePathLatencyMaxValue() {
		metricSourcePathHardAvoidLatency.Add(1)
		return false
	}
	if c.jitter > sourcePathJitterMaxValue() {
		metricSourcePathHardAvoidJitter.Add(1)
		return false
	}
	if c.loss > sourcePathLossMaxValue() {
		metricSourcePathHardAvoidLoss.Add(1)
		return false
	}

	weights := sourcePathScoreWeightsValue()
	c.score = sourcePathQualityScore(c.latency, c.jitter, c.loss, weights)
	if primaryRTT <= 0 {
		return true
	}

	primaryScore := sourcePathQualityScore(primaryRTT, 0, 0, weights)
	needed := primaryScore * (1 + float64(sourcePathScoreImprovePctValue())/100)
	return c.score > needed
}

func sourcePathQualityScore(latency, jitter time.Duration, loss float64, weights sourcePathScoreWeights) float64 {
	latNorm := sourcePathExpNorm(float64(latency), float64(sourcePathLatencyMaxValue()))
	jitterNorm := sourcePathExpNorm(float64(jitter), float64(sourcePathJitterMaxValue()))
	lossNorm := sourcePathExpNorm(loss, sourcePathLossMaxValue())
	return latNorm*weights.latency + jitterNorm*weights.jitter + lossNorm*weights.loss
}

func sourcePathExpNorm(v, maxV float64) float64 {
	if maxV <= 0 {
		return 1
	}
	x := v / maxV
	if x < 0 {
		x = 0
	}
	if x > 1 {
		x = 1
	}
	return 1 / math.Exp(4*x)
}

func sourcePathFlowIDFromBatch(buffs [][]byte, offset int) (uint64, bool) {
	if len(buffs) == 0 || offset < 0 || len(buffs[0]) <= offset {
		return 0, false
	}
	return sourcePathFlowIDFromPacket(buffs[0][offset:])
}

func sourcePathFlowIDFromPacket(b []byte) (uint64, bool) {
	if len(b) < 4 {
		return 0, false
	}
	msgType := binary.LittleEndian.Uint32(b[:4])
	if msgType == device.MessageTransportType && len(b) >= 16 {
		// Without an upstream inner-flow hint, magicsock can only derive a
		// packet-level fallback from the visible WireGuard transport header.
		// The explicit sourcePathDataSendSourceForFlow path below remains the
		// sticky path for a future real 5-tuple hint.
		h := fnv.New64a()
		_, _ = h.Write(b[:16])
		return h.Sum64(), true
	}
	h := fnv.New64a()
	n := min(len(b), 32)
	_, _ = h.Write(b[:n])
	return h.Sum64(), true
}

func (pm *sourcePathProbeManager) assignFlowLocked(key sourcePathFlowKey, source sourceRxMeta, now mono.Time) {
	if pm.flowMap == nil {
		pm.flowMap = make(map[sourcePathFlowKey]sourcePathFlowState)
	}
	pm.pruneExpiredFlowsLocked(now)
	if maxEntries := sourcePathFlowMaxEntriesValue(); maxEntries > 0 {
		for len(pm.flowMap) >= maxEntries {
			if !pm.evictOldestFlowLocked() {
				break
			}
		}
	}
	pm.flowMap[key] = sourcePathFlowState{source: source, lastActivity: now}
	if source.isPrimary() {
		metricSourcePathFlowAssignedPrimary.Add(1)
	} else {
		metricSourcePathFlowAssignedAux.Add(1)
	}
}

func (pm *sourcePathProbeManager) lookupFlowLocked(key sourcePathFlowKey, now mono.Time) (sourcePathFlowState, bool) {
	if pm.flowMap == nil {
		return sourcePathFlowState{}, false
	}
	pm.pruneExpiredFlowsLocked(now)
	st, ok := pm.flowMap[key]
	if !ok {
		return sourcePathFlowState{}, false
	}
	st.lastActivity = now
	pm.flowMap[key] = st
	return st, true
}

func (pm *sourcePathProbeManager) forgetFlowLocked(key sourcePathFlowKey, source sourceRxMeta) {
	if pm.flowMap == nil {
		return
	}
	if st, ok := pm.flowMap[key]; ok && st.source == source {
		delete(pm.flowMap, key)
	}
}

func (pm *sourcePathProbeManager) pruneExpiredFlowsLocked(now mono.Time) {
	if len(pm.flowMap) == 0 {
		return
	}
	idle := sourcePathFlowIdleValue()
	if idle <= 0 {
		return
	}
	var expired int64
	for key, st := range pm.flowMap {
		if now.Sub(st.lastActivity) > idle {
			delete(pm.flowMap, key)
			expired++
		}
	}
	if expired > 0 {
		metricSourcePathFlowEvictedIdle.Add(expired)
	}
}

func (pm *sourcePathProbeManager) evictOldestFlowLocked() bool {
	var (
		oldestKey sourcePathFlowKey
		oldestAt  mono.Time
		found     bool
	)
	for key, st := range pm.flowMap {
		if !found || st.lastActivity.Sub(oldestAt) < 0 {
			oldestKey = key
			oldestAt = st.lastActivity
			found = true
		}
	}
	if !found {
		return false
	}
	delete(pm.flowMap, oldestKey)
	metricSourcePathFlowEvictedCap.Add(1)
	return true
}

func (pm *sourcePathProbeManager) dropFlowsLocked(dst epAddr, source sourceRxMeta) int {
	if len(pm.flowMap) == 0 {
		return 0
	}
	dropped := 0
	for key, st := range pm.flowMap {
		if key.dst == dst && st.source == source {
			delete(pm.flowMap, key)
			dropped++
		}
	}
	return dropped
}

func (pm *sourcePathProbeManager) nextFlowRRLocked(dst epAddr) uint64 {
	if pm.flowRR == nil {
		pm.flowRR = make(map[epAddr]uint64)
	}
	v := pm.flowRR[dst]
	pm.flowRR[dst] = v + 1
	return v
}

func sourcePathFlowChooseAuxByWeight(flowID uint64, auxWeight, primaryWeight float64) bool {
	total := auxWeight + primaryWeight
	if total <= 0 {
		return sourcePathMix64(flowID)&1 == 1
	}
	const buckets = 10000
	cutoff := uint64((auxWeight / total) * buckets)
	return sourcePathMix64(flowID)%buckets < cutoff
}

func sourcePathMix64(v uint64) uint64 {
	v ^= v >> 30
	v *= 0xbf58476d1ce4e5b9
	v ^= v >> 27
	v *= 0x94d049bb133111eb
	v ^= v >> 31
	return v
}

// invalidateLocked drops all samples for the given (dst, source) pair. Used
// when a real-data send via that pair fails so the next selection cycle waits
// for fresh probe evidence before steering data back to it.
func (pm *sourcePathProbeManager) invalidateLocked(dst epAddr, source sourceRxMeta) int {
	n := 0
	dropped := 0
	for _, sample := range pm.samples {
		if sample.dst == dst && sample.source == source {
			dropped++
			continue
		}
		pm.samples[n] = sample
		n++
	}
	pm.samples = pm.samples[:n]
	return dropped
}

func (c *Conn) noteSourcePathDualSendAuxSuccess(dst epAddr, source sourceRxMeta) {
	key := sourcePathDualSendKey{dst: dst, source: source}
	c.mu.Lock()
	delete(c.sourceProbes.dualSendAuxFailStreak, key)
	c.mu.Unlock()
}

func (c *Conn) noteSourcePathDualSendAuxFailure(dst epAddr, source sourceRxMeta, now mono.Time) {
	key := sourcePathDualSendKey{dst: dst, source: source}
	dropStreak := sourcePathDualSendAuxDropStreakValue()
	recovery := sourcePathDualSendRecoveryValue()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sourceProbes.dualSendAuxFailStreak == nil {
		c.sourceProbes.dualSendAuxFailStreak = make(map[sourcePathDualSendKey]int)
	}
	streak := c.sourceProbes.dualSendAuxFailStreak[key] + 1
	c.sourceProbes.dualSendAuxFailStreak[key] = streak
	if streak < dropStreak {
		return
	}
	delete(c.sourceProbes.dualSendAuxFailStreak, key)
	if c.sourceProbes.dualSendDemotedAuxTill == nil {
		c.sourceProbes.dualSendDemotedAuxTill = make(map[sourcePathDualSendKey]mono.Time)
	}
	c.sourceProbes.dualSendDemotedAuxTill[key] = now.Add(recovery)
	metricSourcePathDualSendDemotedAuxStreak.Add(1)
}

func (pm *sourcePathProbeManager) dualSendDemotedLocked(key sourcePathDualSendKey, now mono.Time) bool {
	if pm.dualSendDemotedAuxTill == nil {
		return false
	}
	until, ok := pm.dualSendDemotedAuxTill[key]
	if !ok {
		return false
	}
	if !now.Before(until) {
		delete(pm.dualSendDemotedAuxTill, key)
		return false
	}
	return true
}

func (pm *sourcePathProbeManager) pruneExpiredLocked(now mono.Time) {
	var expired int64
	for txid, tx := range pm.pending {
		if now.Sub(tx.at) >= pingTimeoutDuration {
			delete(pm.pending, txid)
			pm.noteOutcomeLocked(tx.dst, tx.source, now, true)
			expired++
		}
	}
	if expired > 0 {
		metricSourcePathProbePendingExpired.Add(expired)
	}
}

// pruneExpiredSamplesLocked drops samples older than sourcePathSampleTTL.
// Called on every Pong so the slice does not grow with stale samples that
// the scorer would skip anyway.
func (pm *sourcePathProbeManager) pruneExpiredSamplesLocked(now mono.Time) {
	if len(pm.samples) == 0 {
		return
	}
	n := 0
	expired := int64(0)
	for _, sample := range pm.samples {
		if now.Sub(sample.at) > sourcePathSampleTTLValue() {
			expired++
			continue
		}
		pm.samples[n] = sample
		n++
	}
	pm.samples = pm.samples[:n]
	if expired > 0 {
		metricSourcePathProbeSamplesExpired.Add(expired)
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
		pm.noteOutcomeLocked(tx.dst, tx.source, now, true)
		metricSourcePathProbePongExpired.Add(1)
		return true
	}
	if tx.source != rxMeta || tx.dstDisco != sender {
		return false
	}
	delete(pm.pending, txid)
	pm.noteOutcomeLocked(tx.dst, tx.source, now, false)
	pm.pruneExpiredSamplesLocked(now)

	pm.samples = append(pm.samples, sourcePathProbeSample{
		txid:     txid,
		dst:      tx.dst,
		pongFrom: src,
		pongSrc:  pong.Src,
		source:   rxMeta,
		latency:  now.Sub(tx.at),
		at:       now,
	})
	if hardCap := sourcePathProbeSampleLimitCount(); hardCap > 0 && len(pm.samples) > hardCap {
		dropped := int64(len(pm.samples) - hardCap)
		copy(pm.samples, pm.samples[len(pm.samples)-hardCap:])
		pm.samples = pm.samples[:hardCap]
		metricSourcePathProbeSamplesEvicted.Add(dropped)
	}
	metricSourcePathProbePongAccepted.Add(1)
	return true
}

func (pm *sourcePathProbeManager) noteOutcomeLocked(dst epAddr, source sourceRxMeta, at mono.Time, lost bool) {
	pm.outcomes = append(pm.outcomes, sourcePathProbeOutcome{
		dst:    dst,
		source: source,
		at:     at,
		lost:   lost,
	})
	pm.pruneExpiredOutcomesLocked(at)
	if hardCap := sourcePathProbeOutcomeLimitCount(); hardCap > 0 && len(pm.outcomes) > hardCap {
		dropped := int64(len(pm.outcomes) - hardCap)
		copy(pm.outcomes, pm.outcomes[len(pm.outcomes)-hardCap:])
		pm.outcomes = pm.outcomes[:hardCap]
		metricSourcePathProbeOutcomesEvicted.Add(dropped)
	}
}

func (pm *sourcePathProbeManager) pruneExpiredOutcomesLocked(now mono.Time) {
	if len(pm.outcomes) == 0 {
		return
	}
	window := sourcePathLossWindowValue()
	n := 0
	for _, outcome := range pm.outcomes {
		if now.Sub(outcome.at) > window {
			continue
		}
		pm.outcomes[n] = outcome
		n++
	}
	pm.outcomes = pm.outcomes[:n]
}

func (pm *sourcePathProbeManager) lossRatioLocked(dst epAddr, source sourceRxMeta, now mono.Time) float64 {
	pm.pruneExpiredOutcomesLocked(now)
	var total, lost int
	for _, outcome := range pm.outcomes {
		if outcome.dst != dst || outcome.source != source {
			continue
		}
		total++
		if outcome.lost {
			lost++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(lost) / float64(total)
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
		// Track padded source-path probes separately. They share the
		// padding mechanism with disco peer-MTU probes but are not the
		// same thing; folding them into peerMTU* counters poisons the
		// MTU dashboard once srcsel is enabled.
		metricSentDiscoSourcePathProbePadded.Add(1)
		metricSentDiscoSourcePathProbeBytes.Add(int64(pingSizeToPktLen(size, dst)))
	}
	return true, nil
}

func sourcePathBindError(errs ...error) error {
	return errors.Join(errs...)
}
