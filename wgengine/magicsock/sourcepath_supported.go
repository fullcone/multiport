// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux || windows

package magicsock

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/tailscale/wireguard-go/conn"
	"tailscale.com/envknob"
	"tailscale.com/tstime/mono"
	"tailscale.com/types/opt"
)

var (
	envknobSrcSelEnable              = envknob.RegisterOptBool("TS_EXPERIMENTAL_SRCSEL_ENABLE")
	envknobSrcSelAuxSockets          = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS")
	envknobSrcSelForceDataSource     = envknob.RegisterString("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE")
	envknobSrcSelAutoDataSource      = envknob.RegisterBool("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE")
	envknobSrcSelMaxPeers            = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_MAX_PEERS")
	envknobSrcSelMaxProbeBurst       = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST")
	envknobSrcSelMaxPending          = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_MAX_PENDING")
	envknobSrcSelMaxSamples          = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_MAX_SAMPLES")
	envknobSrcSelMaxOutcomes         = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_MAX_OUTCOMES")
	envknobSrcSelAuxBeatThresholdPct = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT")
	envknobSrcSelDualSend            = envknob.RegisterOptBool("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND")
	envknobSrcSelDualSendAuxDrop     = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_AUX_DROP_STREAK")
	envknobSrcSelDualSendRecoveryS   = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_RECOVERY_S")
	envknobSrcSelDualSendMaxSkewMS   = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_MAX_SKEW_MS")
	envknobSrcSelActiveBackup        = envknob.RegisterBool("TS_EXPERIMENTAL_SRCSEL_ACTIVE_BACKUP")
	envknobSrcSelPrimaryFailStreak   = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_PRIMARY_FAIL_STREAK")
	envknobSrcSelFailoverHoldS       = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_FAILOVER_HOLD_S")
	envknobSrcSelFailoverRecovery    = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_FAILOVER_RECOVERY_PONGS")
	envknobSrcSelMultiMetric         = envknob.RegisterBool("TS_EXPERIMENTAL_SRCSEL_MULTI_METRIC")
	envknobSrcSelProbeIntervalMS     = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_PROBE_INTERVAL_MS")
	envknobSrcSelLossWindowS         = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_LOSS_WINDOW_S")
	envknobSrcSelScoreWeights        = envknob.RegisterString("TS_EXPERIMENTAL_SRCSEL_SCORE_WEIGHTS")
	envknobSrcSelLatencyMaxMS        = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_LATENCY_MAX_MS")
	envknobSrcSelJitterMaxMS         = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_JITTER_MAX_MS")
	envknobSrcSelLossMaxPct          = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_LOSS_MAX_PCT")
	envknobSrcSelScoreImprovePct     = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_SCORE_IMPROVEMENT_PCT")
	envknobSrcSelProfile             = envknob.RegisterString("TS_EXPERIMENTAL_SRCSEL_PROFILE")
	envknobSrcSelSampleTTLS          = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_SAMPLE_TTL_S")
	envknobSrcSelFlowAware           = envknob.RegisterBool("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE")
	envknobSrcSelBalancePolicy       = envknob.RegisterString("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY")
	envknobSrcSelFlowIdleS           = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_FLOW_IDLE_S")
	envknobSrcSelFlowMax             = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_FLOW_MAX")
)

func sourcePathOptBoolDefaultTrue(v opt.Bool) bool {
	if b, ok := v.Get(); ok {
		return b
	}
	return true
}

func sourcePathEnabled() bool {
	return sourcePathOptBoolDefaultTrue(envknobSrcSelEnable())
}

func sourcePathAuxSocketCount() int {
	if !sourcePathEnabled() {
		return 0
	}
	n, ok := envknob.LookupInt("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS")
	if !ok {
		return 1
	}
	if n < 0 {
		return 0
	}
	return min(n, 1)
}

// sourcePathProbeMaxPeerCount returns the policy cap on distinct peers with
// pending probes. Zero means "unlimited" — the default for server deployments
// where the probe traffic itself is negligible. A positive
// TS_EXPERIMENTAL_SRCSEL_MAX_PEERS sets an explicit cap.
func sourcePathProbeMaxPeerCount() int {
	n := envknobSrcSelMaxPeers()
	if n < 0 {
		return 0
	}
	if n == 0 {
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

// sourcePathProbeHardPendingCount is the memory-safety hard cap on total
// pending probes. <= 0 disables the cap (tests only).
func sourcePathProbeHardPendingCount() int {
	n := envknobSrcSelMaxPending()
	if n == 0 {
		return sourcePathProbeHardPendingCap
	}
	return n
}

// sourcePathProbeSampleLimitCount is the memory-safety hard cap on the total
// number of probe samples retained globally. <= 0 disables the cap.
func sourcePathProbeSampleLimitCount() int {
	n := envknobSrcSelMaxSamples()
	if n == 0 {
		return sourcePathProbeHistoryLimit
	}
	return n
}

// sourcePathProbeOutcomeLimitCount is the memory-safety hard cap on the total
// number of probe outcomes retained globally for loss scoring. <= 0 disables
// the cap.
func sourcePathProbeOutcomeLimitCount() int {
	n := envknobSrcSelMaxOutcomes()
	if n == 0 {
		return sourcePathProbeOutcomeLimit
	}
	return n
}

// sourcePathAuxBeatThresholdPercentValue returns the percent by which an
// auxiliary candidate must beat the primary path's RTT before automatic
// selection is allowed to use it.
//
//	env <  0  → 0 (primary-baseline comparison disabled)
//	env == 0  → default sourcePathAuxBeatThresholdPercent (also the unset case
//	             since envknob.Int returns 0 for missing variables)
//	env >  0  → that value, clamped to [1, 100]
//
// Callers treat a return value of 0 as "skip the primary-baseline gate
// entirely" — useful for tests and for opt-out under operational control.
func sourcePathAuxBeatThresholdPercentValue() int {
	n := envknobSrcSelAuxBeatThresholdPct()
	if n < 0 {
		return 0
	}
	if n == 0 {
		return sourcePathAuxBeatThresholdPercent
	}
	if n > 100 {
		return 100
	}
	return n
}

func sourcePathProfileMode() string {
	return strings.ToLower(envknobSrcSelProfile())
}

func sourcePathMultiMetricEnabled() bool {
	return envknobSrcSelMultiMetric() || sourcePathProfileMode() == "realtime"
}

func sourcePathProbeIntervalValue() time.Duration {
	n, ok := envknob.LookupInt("TS_EXPERIMENTAL_SRCSEL_PROBE_INTERVAL_MS")
	if !ok {
		return sourcePathRealtimeProbeEvery
	}
	if n == 0 && sourcePathProfileMode() == "realtime" {
		return sourcePathRealtimeProbeEvery
	}
	if n <= 0 {
		return 0
	}
	d := time.Duration(n) * time.Millisecond
	if d < sourcePathProbeIntervalFloor {
		return sourcePathProbeIntervalFloor
	}
	return d
}

func sourcePathSampleTTLValue() time.Duration {
	n := envknobSrcSelSampleTTLS()
	if n > 0 {
		return time.Duration(n) * time.Second
	}
	if sourcePathProfileMode() == "realtime" {
		return sourcePathRealtimeSampleTTL
	}
	return sourcePathSampleTTL
}

func sourcePathLossWindowValue() time.Duration {
	n := envknobSrcSelLossWindowS()
	if n <= 0 {
		return sourcePathLossWindow
	}
	return time.Duration(n) * time.Second
}

func sourcePathLatencyMaxValue() time.Duration {
	n := envknobSrcSelLatencyMaxMS()
	if n <= 0 {
		return sourcePathLatencyMax
	}
	return time.Duration(n) * time.Millisecond
}

func sourcePathJitterMaxValue() time.Duration {
	n := envknobSrcSelJitterMaxMS()
	if n <= 0 {
		return sourcePathJitterMax
	}
	return time.Duration(n) * time.Millisecond
}

func sourcePathLossMaxValue() float64 {
	n := envknobSrcSelLossMaxPct()
	if n <= 0 {
		return sourcePathLossMax
	}
	return float64(n) / 100
}

func sourcePathScoreImprovePctValue() int {
	n := envknobSrcSelScoreImprovePct()
	if n <= 0 {
		return sourcePathScoreImprovePct
	}
	return n
}

func sourcePathScoreWeightsValue() sourcePathScoreWeights {
	weights := sourcePathScoreWeights{
		latency: 0.30,
		jitter:  0.40,
		loss:    0.30,
	}
	for _, field := range strings.Split(envknobSrcSelScoreWeights(), ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || f < 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "lat", "latency":
			weights.latency = f
		case "jit", "jitter":
			weights.jitter = f
		case "loss":
			weights.loss = f
		}
	}
	return weights
}

func sourcePathDualSendEnabled() bool {
	return sourcePathOptBoolDefaultTrue(envknobSrcSelDualSend()) && sourcePathAuxSocketCount() > 0
}

func sourcePathDualSendAuxDropStreakValue() int {
	n := envknobSrcSelDualSendAuxDrop()
	if n <= 0 {
		return sourcePathDualSendAuxDropStreak
	}
	return n
}

func sourcePathDualSendRecoveryValue() time.Duration {
	n := envknobSrcSelDualSendRecoveryS()
	if n <= 0 {
		return sourcePathDualSendRecovery
	}
	return time.Duration(n) * time.Second
}

func sourcePathDualSendMaxSkewValue() time.Duration {
	n := envknobSrcSelDualSendMaxSkewMS()
	if n <= 0 {
		return sourcePathDualSendMaxSkew
	}
	return time.Duration(n) * time.Millisecond
}

func sourcePathActiveBackupEnabled() bool {
	return envknobSrcSelActiveBackup() && sourcePathAuxSocketCount() > 0
}

func sourcePathActiveBackupPrimaryFailStreakValue() int {
	n := envknobSrcSelPrimaryFailStreak()
	if n <= 0 {
		return sourcePathActiveBackupPrimaryFailStreak
	}
	return n
}

func sourcePathActiveBackupFailoverHoldValue() time.Duration {
	n := envknobSrcSelFailoverHoldS()
	if n < 0 {
		return 0
	}
	if n == 0 {
		return sourcePathActiveBackupFailoverHold
	}
	return time.Duration(n) * time.Second
}

func sourcePathActiveBackupRecoveryPongsValue() int {
	n := envknobSrcSelFailoverRecovery()
	if n <= 0 {
		return sourcePathActiveBackupRecoveryPongs
	}
	return n
}

func sourcePathFlowAwareEnabled() bool {
	return envknobSrcSelFlowAware() && sourcePathAuxSocketCount() > 0
}

func sourcePathBalancePolicyValue() string {
	switch strings.ToLower(envknobSrcSelBalancePolicy()) {
	case "rr", "xor", "aware":
		return strings.ToLower(envknobSrcSelBalancePolicy())
	default:
		return "aware"
	}
}

func sourcePathFlowIdleValue() time.Duration {
	n := envknobSrcSelFlowIdleS()
	if n < 0 {
		return 0
	}
	if n == 0 {
		return sourcePathFlowIdle
	}
	return time.Duration(n) * time.Second
}

func sourcePathFlowMaxEntriesValue() int {
	n := envknobSrcSelFlowMax()
	if n < 0 {
		return 0
	}
	if n == 0 {
		return sourcePathFlowMaxEntries
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
	if source, ok := c.sourcePathActiveBackupCandidate(dst, mono.Now()); ok {
		return source
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

func (c *Conn) sourcePathDataSendSourceForBatch(dst epAddr, buffs [][]byte, offset int) sourceRxMeta {
	if !sourcePathFlowAwareEnabled() {
		return c.sourcePathDataSendSource(dst)
	}
	flowID, ok := sourcePathFlowIDFromBatch(buffs, offset)
	if !ok {
		metricSourcePathFlowHintUnavailable.Add(1)
		return c.sourcePathDataSendSource(dst)
	}
	return c.sourcePathDataSendSourceForFlow(dst, flowID, mono.Now())
}

func (c *Conn) sourcePathDataSendSourceForFlow(dst epAddr, flowID uint64, now mono.Time) sourceRxMeta {
	if !sourcePathFlowAwareEnabled() || !dst.isDirect() || flowID == 0 {
		return c.sourcePathDataSendSource(dst)
	}
	if source, ok := c.sourcePathActiveBackupCandidate(dst, now); ok {
		return source
	}
	if forceMode := sourcePathForcedDataSourceMode(); forceMode != "" {
		if !sourcePathForcedDataSourceModeAllowsAddr(forceMode, dst.ap.Addr()) {
			return primarySourceRxMeta
		}
		return c.sourcePathForcedDataSendSource(dst)
	}

	key := sourcePathFlowKey{dst: dst, id: flowID}
	c.mu.Lock()
	st, ok := c.sourceProbes.lookupFlowLocked(key, now)
	c.mu.Unlock()
	if ok {
		if st.source.isPrimary() || c.sourcePathSourceAvailable(dst, st.source) {
			return st.source
		}
		c.mu.Lock()
		c.sourceProbes.forgetFlowLocked(key, st.source)
		c.mu.Unlock()
	}

	score, auxOK := c.sourcePathBestCandidate(dst)
	source := c.sourcePathNewFlowSource(dst, flowID, score, auxOK)
	c.mu.Lock()
	c.sourceProbes.assignFlowLocked(key, source, now)
	c.mu.Unlock()
	return source
}

func (c *Conn) sourcePathNewFlowSource(dst epAddr, flowID uint64, aux sourcePathCandidateScore, auxOK bool) sourceRxMeta {
	if !auxOK {
		return primarySourceRxMeta
	}
	switch sourcePathBalancePolicyValue() {
	case "rr":
		c.mu.Lock()
		rr := c.sourceProbes.nextFlowRRLocked(dst)
		c.mu.Unlock()
		if rr%2 == 0 {
			return primarySourceRxMeta
		}
		return aux.source
	case "xor":
		if sourcePathMix64(flowID)&1 == 0 {
			return primarySourceRxMeta
		}
		return aux.source
	default:
		auxWeight := 1.0
		primaryWeight := 1.0
		if sourcePathMultiMetricEnabled() {
			weights := sourcePathScoreWeightsValue()
			auxWeight = aux.score
			if auxWeight <= 0 {
				auxWeight = sourcePathQualityScore(aux.latency, aux.jitter, aux.loss, weights)
			}
			primaryRTT := c.primaryRTTForDst(dst)
			if primaryRTT > 0 {
				primaryWeight = sourcePathQualityScore(primaryRTT, 0, 0, weights)
			}
		}
		if sourcePathFlowChooseAuxByWeight(flowID, auxWeight, primaryWeight) {
			return aux.source
		}
		return primarySourceRxMeta
	}
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
