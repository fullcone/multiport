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
	envknobSrcSelEnable               = envknob.RegisterOptBool("TS_EXPERIMENTAL_SRCSEL_ENABLE")
	envknobSrcSelAuxSockets           = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS")
	envknobSrcSelDataStrategy         = envknob.RegisterString("TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY")
	envknobSrcSelForceDataSource      = envknob.RegisterString("TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE")
	envknobSrcSelAutoDataSource       = envknob.RegisterBool("TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE")
	envknobSrcSelMaxPeers             = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_MAX_PEERS")
	envknobSrcSelMaxProbeBurst        = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST")
	envknobSrcSelMaxPending           = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_MAX_PENDING")
	envknobSrcSelMaxSamples           = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_MAX_SAMPLES")
	envknobSrcSelMaxOutcomes          = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_MAX_OUTCOMES")
	envknobSrcSelAuxBeatThresholdPct  = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT")
	envknobSrcSelDualSend             = envknob.RegisterOptBool("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND")
	envknobSrcSelDualSendAuxDrop      = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_AUX_DROP_STREAK")
	envknobSrcSelDualSendAuxProbeDrop = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_AUX_PROBE_DROP_STREAK")
	envknobSrcSelDualSendRecoveryS    = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_RECOVERY_S")
	envknobSrcSelDualSendMaxSkewMS    = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_MAX_SKEW_MS")
	envknobSrcSelActiveBackup         = envknob.RegisterBool("TS_EXPERIMENTAL_SRCSEL_ACTIVE_BACKUP")
	envknobSrcSelPrimaryFailStreak    = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_PRIMARY_FAIL_STREAK")
	envknobSrcSelFailoverHoldS        = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_FAILOVER_HOLD_S")
	envknobSrcSelFailoverRecovery     = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_FAILOVER_RECOVERY_PONGS")
	envknobSrcSelMultiMetric          = envknob.RegisterBool("TS_EXPERIMENTAL_SRCSEL_MULTI_METRIC")
	envknobSrcSelProbeIntervalMS      = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_PROBE_INTERVAL_MS")
	envknobSrcSelLossWindowS          = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_LOSS_WINDOW_S")
	envknobSrcSelScoreWeights         = envknob.RegisterString("TS_EXPERIMENTAL_SRCSEL_SCORE_WEIGHTS")
	envknobSrcSelLatencyMaxMS         = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_LATENCY_MAX_MS")
	envknobSrcSelJitterMaxMS          = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_JITTER_MAX_MS")
	envknobSrcSelLossMaxPct           = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_LOSS_MAX_PCT")
	envknobSrcSelScoreImprovePct      = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_SCORE_IMPROVEMENT_PCT")
	envknobSrcSelProfile              = envknob.RegisterString("TS_EXPERIMENTAL_SRCSEL_PROFILE")
	envknobSrcSelSampleTTLS           = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_SAMPLE_TTL_S")
	envknobSrcSelFlowAware            = envknob.RegisterBool("TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE")
	envknobSrcSelBalancePolicy        = envknob.RegisterString("TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY")
	envknobSrcSelFlowIdleS            = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_FLOW_IDLE_S")
	envknobSrcSelFlowMax              = envknob.RegisterInt("TS_EXPERIMENTAL_SRCSEL_FLOW_MAX")
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

func sourcePathDataStrategyMode() string {
	switch strings.ToLower(strings.TrimSpace(envknobSrcSelDataStrategy())) {
	case "", "dual", "dual-send", "dual_send", "redundant":
		return sourcePathDataStrategyDualSend
	case "dual-endpoint", "dual_endpoint", "endpoint", "endpoints", "redundant-endpoint", "redundant_endpoint", "multipath":
		return sourcePathDataStrategyDualEndpoint
	case "single", "single-source", "single_source", "auto", "flow", "flow-aware", "flow_aware", "rr", "round-robin", "round_robin":
		return sourcePathDataStrategySingleSource
	case "active-backup", "active_backup", "backup", "failover":
		return sourcePathDataStrategyActiveBackup
	default:
		return sourcePathDataStrategyDualSend
	}
}

func sourcePathDualEndpointStrategyEnabled() bool {
	return sourcePathDataStrategyMode() == sourcePathDataStrategyDualEndpoint
}

func sourcePathSingleSourceStrategyEnabled() bool {
	return sourcePathDataStrategyMode() == sourcePathDataStrategySingleSource
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
	if n > sourcePathMaxAuxSockets {
		return sourcePathMaxAuxSockets
	}
	return n
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
		return max(sourcePathProbeMaxBurst, sourcePathAuxSocketCount()*2)
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
	return sourcePathDataStrategyMode() == sourcePathDataStrategyDualSend &&
		sourcePathOptBoolDefaultTrue(envknobSrcSelDualSend()) &&
		sourcePathAuxSocketCount() > 0
}

func sourcePathDualSendAuxDropStreakValue() int {
	n := envknobSrcSelDualSendAuxDrop()
	if n <= 0 {
		return sourcePathDualSendAuxDropStreak
	}
	return n
}

func sourcePathDualSendAuxProbeDropStreakValue() int {
	n := envknobSrcSelDualSendAuxProbeDrop()
	if n <= 0 {
		return sourcePathDualSendAuxProbeDropStreak
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
	return sourcePathDataStrategyMode() == sourcePathDataStrategyActiveBackup &&
		envknobSrcSelActiveBackup() &&
		sourcePathAuxSocketCount() > 0
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
	return sourcePathSingleSourceStrategyEnabled() &&
		envknobSrcSelFlowAware() &&
		sourcePathAuxSocketCount() > 0
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
	count := sourcePathAuxSocketCount()
	if count == 0 {
		return nil
	}

	c.sourcePath.mu.Lock()
	defer c.sourcePath.mu.Unlock()
	c.ensureSourcePathAuxSocketCountLocked(count)

	fns := make([]conn.ReceiveFunc, 0, count*2)
	c.forEachSourcePathSocketLocked(true, func(_ int, sock *sourcePathSocket, _ *bool) {
		c.ensureSourcePathPConnLocked(&sock.pconn)
		fns = append(fns, c.mkReceiveFuncWithSource(&sock.pconn, nil, nil, nil, nil, nil, sock.rxMeta))
	})
	c.forEachSourcePathSocketLocked(false, func(_ int, sock *sourcePathSocket, _ *bool) {
		c.ensureSourcePathPConnLocked(&sock.pconn)
		fns = append(fns, c.mkReceiveFuncWithSource(&sock.pconn, nil, nil, nil, nil, nil, sock.rxMeta))
	})
	return fns
}

func (c *Conn) sourcePathProbeSources(is4 bool) []sourceRxMeta {
	if sourcePathAuxSocketCount() == 0 {
		return nil
	}
	c.sourcePath.mu.Lock()
	defer c.sourcePath.mu.Unlock()
	var sources []sourceRxMeta
	c.forEachSourcePathSocketLocked(is4, func(_ int, sock *sourcePathSocket, bound *bool) {
		if *bound {
			sources = append(sources, sock.rxMeta())
		}
	})
	return sources
}

func sourcePathAuxSocketID(is4 bool, index int) SourceSocketID {
	if index == 0 {
		if is4 {
			return sourceIPv4SocketID
		}
		return sourceIPv6SocketID
	}
	if is4 {
		return sourceIPv4ExtraSocketIDBase + SourceSocketID(index-1)
	}
	return sourceIPv6ExtraSocketIDBase + SourceSocketID(index-1)
}

func (c *Conn) ensureSourcePathAuxSocketCountLocked(count int) {
	if count < 1 {
		count = 1
	}
	if count > sourcePathMaxAuxSockets {
		count = sourcePathMaxAuxSockets
	}
	c.sourcePath.aux4.setID(sourcePathAuxSocketID(true, 0))
	c.sourcePath.aux6.setID(sourcePathAuxSocketID(false, 0))

	wantExtra := count - 1
	for len(c.sourcePath.extraAux4) > wantExtra {
		last := len(c.sourcePath.extraAux4) - 1
		c.sourcePath.extra4Bound[last] = false
		c.closeSourcePathPConnLocked(&c.sourcePath.extraAux4[last].pconn)
		c.sourcePath.extraAux4 = c.sourcePath.extraAux4[:last]
		c.sourcePath.extra4Bound = c.sourcePath.extra4Bound[:last]
	}
	for len(c.sourcePath.extraAux6) > wantExtra {
		last := len(c.sourcePath.extraAux6) - 1
		c.sourcePath.extra6Bound[last] = false
		c.closeSourcePathPConnLocked(&c.sourcePath.extraAux6[last].pconn)
		c.sourcePath.extraAux6 = c.sourcePath.extraAux6[:last]
		c.sourcePath.extra6Bound = c.sourcePath.extra6Bound[:last]
	}
	for len(c.sourcePath.extraAux4) < wantExtra {
		c.sourcePath.extraAux4 = append(c.sourcePath.extraAux4, sourcePathSocket{})
		c.sourcePath.extra4Bound = append(c.sourcePath.extra4Bound, false)
	}
	for len(c.sourcePath.extraAux6) < wantExtra {
		c.sourcePath.extraAux6 = append(c.sourcePath.extraAux6, sourcePathSocket{})
		c.sourcePath.extra6Bound = append(c.sourcePath.extra6Bound, false)
	}
	for i := range c.sourcePath.extraAux4 {
		c.sourcePath.extraAux4[i].setID(sourcePathAuxSocketID(true, i+1))
	}
	for i := range c.sourcePath.extraAux6 {
		c.sourcePath.extraAux6[i].setID(sourcePathAuxSocketID(false, i+1))
	}
}

func (c *Conn) forEachSourcePathSocketLocked(is4 bool, fn func(index int, sock *sourcePathSocket, bound *bool)) {
	if is4 {
		fn(0, &c.sourcePath.aux4, &c.sourcePath.aux4Bound)
		for i := range c.sourcePath.extraAux4 {
			fn(i+1, &c.sourcePath.extraAux4[i], &c.sourcePath.extra4Bound[i])
		}
		return
	}
	fn(0, &c.sourcePath.aux6, &c.sourcePath.aux6Bound)
	for i := range c.sourcePath.extraAux6 {
		fn(i+1, &c.sourcePath.extraAux6[i], &c.sourcePath.extra6Bound[i])
	}
}

func (c *Conn) sourcePathSocketSlotLocked(id SourceSocketID) (sock *sourcePathSocket, bound *bool, network string, ok bool) {
	if id == sourceIPv4SocketID {
		return &c.sourcePath.aux4, &c.sourcePath.aux4Bound, "udp4", true
	}
	if id == sourceIPv6SocketID {
		return &c.sourcePath.aux6, &c.sourcePath.aux6Bound, "udp6", true
	}
	if id >= sourceIPv4ExtraSocketIDBase && id < sourceIPv4ExtraSocketIDBase+SourceSocketID(sourcePathMaxAuxSockets) {
		idx := int(id - sourceIPv4ExtraSocketIDBase)
		if idx >= 0 && idx < len(c.sourcePath.extraAux4) {
			return &c.sourcePath.extraAux4[idx], &c.sourcePath.extra4Bound[idx], "udp4", true
		}
	}
	if id >= sourceIPv6ExtraSocketIDBase && id < sourceIPv6ExtraSocketIDBase+SourceSocketID(sourcePathMaxAuxSockets) {
		idx := int(id - sourceIPv6ExtraSocketIDBase)
		if idx >= 0 && idx < len(c.sourcePath.extraAux6) {
			return &c.sourcePath.extraAux6[idx], &c.sourcePath.extra6Bound[idx], "udp6", true
		}
	}
	return nil, nil, "", false
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

	switch sourcePathDataStrategyMode() {
	case sourcePathDataStrategyActiveBackup:
		if source, ok := c.sourcePathActiveBackupCandidate(dst, mono.Now()); ok {
			return source
		}
		return primarySourceRxMeta
	case sourcePathDataStrategySingleSource:
	default:
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
	var source sourceRxMeta
	c.forEachSourcePathSocketLocked(dst.ap.Addr().Is4(), func(_ int, sock *sourcePathSocket, bound *bool) {
		if source.isPrimary() && *bound {
			source = sock.rxMeta()
		}
	})
	if source.isPrimary() {
		return primarySourceRxMeta
	}
	return source
}

func (c *Conn) sourcePathWriteWireGuardBatchTo(source sourceRxMeta, dst epAddr, buffs [][]byte, offset int) error {
	c.sourcePath.mu.Lock()
	var ruc *RebindingUDPConn
	if dst.isDirect() {
		c.forEachSourcePathSocketLocked(dst.ap.Addr().Is4(), func(_ int, sock *sourcePathSocket, bound *bool) {
			if ruc == nil && *bound && source == sock.rxMeta() {
				ruc = &sock.pconn
			}
		})
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
	c.forEachSourcePathSocketLocked(dst.Addr().Is4(), func(_ int, sock *sourcePathSocket, bound *bool) {
		if ruc == nil && *bound && source == sock.rxMeta() {
			ruc = &sock.pconn
		}
	})
	c.sourcePath.mu.Unlock()
	if ruc == nil {
		return 0, errSourcePathUnavailable
	}
	return ruc.WriteToUDPAddrPort(pkt, dst)
}

func (c *Conn) rebindSourcePathSockets() error {
	count := sourcePathAuxSocketCount()
	if count == 0 {
		c.closeSourcePathSockets()
		c.mu.Lock()
		c.sourceProbes.clearLocked()
		c.mu.Unlock()
		return nil
	}

	c.sourcePath.mu.Lock()
	defer c.sourcePath.mu.Unlock()
	c.ensureSourcePathAuxSocketCountLocked(count)
	c.sourcePath.generation++

	err4 := c.bindSourcePathSocketFamilyLocked(true, "udp4")
	err6 := c.bindSourcePathSocketFamilyLocked(false, "udp6")

	return sourcePathBindError(err4, err6)
}

func (c *Conn) rebindSourcePathSocket(source sourceRxMeta) (sourceRxMeta, bool, error) {
	if sourcePathAuxSocketCount() == 0 || source.isPrimary() {
		return sourceRxMeta{}, false, errSourcePathUnavailable
	}

	c.sourcePath.mu.Lock()
	defer c.sourcePath.mu.Unlock()

	target, bound, network, ok := c.sourcePathSocketSlotLocked(source.socketID)
	if !ok {
		return sourceRxMeta{}, false, errSourcePathUnavailable
	}
	current := target.rxMeta()
	if source != current {
		return current, false, nil
	}

	c.sourcePath.generation++
	target.generation.Store(uint64(c.sourcePath.generation))
	err := c.bindSourcePathSocketLocked(&target.pconn, network)
	*bound = err == nil
	if err != nil {
		c.setSourcePathBlockForeverLocked(&target.pconn)
		return target.rxMeta(), true, err
	}
	return target.rxMeta(), true, nil
}

func (c *Conn) bindSourcePathSocketFamilyLocked(is4 bool, network string) error {
	var firstErr error
	var anyBound bool
	c.forEachSourcePathSocketLocked(is4, func(_ int, sock *sourcePathSocket, bound *bool) {
		sock.generation.Store(uint64(c.sourcePath.generation))
		err := c.bindSourcePathSocketLocked(&sock.pconn, network)
		*bound = err == nil
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			c.setSourcePathBlockForeverLocked(&sock.pconn)
			return
		}
		anyBound = true
	})
	if anyBound {
		return nil
	}
	return firstErr
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
	for i := range c.sourcePath.extraAux4 {
		c.sourcePath.extra4Bound[i] = false
		c.closeSourcePathPConnLocked(&c.sourcePath.extraAux4[i].pconn)
	}
	for i := range c.sourcePath.extraAux6 {
		c.sourcePath.extra6Bound[i] = false
		c.closeSourcePathPConnLocked(&c.sourcePath.extraAux6[i].pconn)
	}
}

func (c *Conn) closeSourcePathPConnLocked(ruc *RebindingUDPConn) {
	if err := ruc.Close(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, errNilPConn) {
		c.logf("magicsock: srcsel: auxiliary close failed: %v", err)
	}
}
