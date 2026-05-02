// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !linux && !windows

package magicsock

import (
	"net/netip"
	"time"

	"github.com/tailscale/wireguard-go/conn"
)

func (c *Conn) sourcePathReceiveFuncs() []conn.ReceiveFunc { return nil }

func sourcePathAuxSocketCount() int { return 0 }

func sourcePathDataStrategyMode() string { return sourcePathDataStrategyDualSend }

func sourcePathDualEndpointStrategyEnabled() bool { return false }

func sourcePathSingleSourceStrategyEnabled() bool { return false }

func (c *Conn) sourcePathProbeSources(is4 bool) []sourceRxMeta { return nil }

func (c *Conn) sourcePathDataSendSource(dst epAddr) sourceRxMeta { return primarySourceRxMeta }

func sourcePathProbeMaxPeerCount() int { return sourcePathProbeMaxPeers }

func sourcePathProbeMaxBurstCount() int { return sourcePathProbeMaxBurst }

func sourcePathProbeHardPendingCount() int { return sourcePathProbeHardPendingCap }

func sourcePathProbeSampleLimitCount() int { return sourcePathProbeHistoryLimit }

func sourcePathProbeOutcomeLimitCount() int { return sourcePathProbeOutcomeLimit }

func sourcePathAuxBeatThresholdPercentValue() int { return sourcePathAuxBeatThresholdPercent }

func sourcePathMultiMetricEnabled() bool { return false }

func sourcePathProbeIntervalValue() time.Duration { return 0 }

func sourcePathSampleTTLValue() time.Duration { return sourcePathSampleTTL }

func sourcePathLossWindowValue() time.Duration { return sourcePathLossWindow }

func sourcePathLatencyMaxValue() time.Duration { return sourcePathLatencyMax }

func sourcePathJitterMaxValue() time.Duration { return sourcePathJitterMax }

func sourcePathLossMaxValue() float64 { return sourcePathLossMax }

func sourcePathScoreImprovePctValue() int { return sourcePathScoreImprovePct }

func sourcePathScoreWeightsValue() sourcePathScoreWeights {
	return sourcePathScoreWeights{latency: 0.30, jitter: 0.40, loss: 0.30}
}

func sourcePathDualSendEnabled() bool { return false }

func sourcePathObservedEndpointFanoutEnabled() bool { return false }

func sourcePathDualSendAuxDropStreakValue() int { return sourcePathDualSendAuxDropStreak }

func sourcePathDualSendRecoveryValue() time.Duration { return sourcePathDualSendRecovery }

func sourcePathDualSendMaxSkewValue() time.Duration { return sourcePathDualSendMaxSkew }

func sourcePathActiveBackupEnabled() bool { return false }

func sourcePathActiveBackupPrimaryFailStreakValue() int {
	return sourcePathActiveBackupPrimaryFailStreak
}

func sourcePathActiveBackupFailoverHoldValue() time.Duration {
	return sourcePathActiveBackupFailoverHold
}

func sourcePathActiveBackupRecoveryPongsValue() int {
	return sourcePathActiveBackupRecoveryPongs
}

func sourcePathFlowAwareEnabled() bool { return false }

func sourcePathBalancePolicyValue() string { return "aware" }

func sourcePathFlowIdleValue() time.Duration { return sourcePathFlowIdle }

func sourcePathFlowMaxEntriesValue() int { return sourcePathFlowMaxEntries }

func (c *Conn) sourcePathDataSendSourceForBatch(dst epAddr, buffs [][]byte, offset int) sourceRxMeta {
	return primarySourceRxMeta
}

func (c *Conn) sourcePathWriteWireGuardBatchTo(source sourceRxMeta, dst epAddr, buffs [][]byte, offset int) error {
	return errSourcePathUnavailable
}

func (c *Conn) sourcePathWriteTo(source sourceRxMeta, dst netip.AddrPort, pkt []byte) (int, error) {
	return 0, errSourcePathUnavailable
}

func (c *Conn) rebindSourcePathSockets() error { return nil }

func (c *Conn) closeSourcePathSockets() {}
