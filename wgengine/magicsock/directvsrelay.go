// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"time"

	"tailscale.com/envknob"
)

// Phase 22 v2 — direct-vs-relay latency-aware switching.
//
// magicsock today unconditionally prefers a direct UDP path over any
// peer-relay path: endpoint.wantUDPRelayPathDiscoveryLocked suppresses
// relay path discovery whenever bestAddr holds a trusted direct path,
// and betterAddr() short-circuits on vni.IsSet() before its points-based
// latency scoring runs. The combination means that even when a peer-relay
// would have lower end-to-end RTT than the direct path (e.g. cross-
// continental BGP-suboptimal direct routes), the relay never gets a
// chance to be measured, much less selected.
//
// When TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE is true, both layers
// switch from "always direct" to a Phase 20-style 10 % relative gate:
// the alternative category must be at least 10 % faster than the current
// choice before the swap is allowed. With the env knob false (default),
// today's behaviour is bit-identical.
//
// Periodic comparison overhead is bounded by directVsRelayCompareInterval
// (default 5 minutes). Per-peer hysteresis (directVsRelayHoldDuration)
// after a swap prevents flapping at the gate boundary.

var (
	envknobDirectVsRelayCompare       = envknob.RegisterBool("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE")
	envknobDirectVsRelayCompareInterv = envknob.RegisterInt("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S")
	envknobDirectVsRelayHold          = envknob.RegisterInt("TS_EXPERIMENTAL_DIRECT_VS_RELAY_HOLD_S")
	envknobDirectVsRelayThresholdPct  = envknob.RegisterInt("TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT")
)

const (
	// directVsRelayCompareInterval is the default minimum spacing between
	// relay-path discovery cycles when a trusted direct path is already
	// held. Operators tune via TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S.
	directVsRelayCompareInterval = 5 * time.Minute

	// directVsRelayHoldDuration is the per-peer hysteresis window after a
	// direct↔relay swap. Within this window no further category swaps for
	// the same peer are allowed regardless of measured latency. Operators
	// tune via TS_EXPERIMENTAL_DIRECT_VS_RELAY_HOLD_S.
	directVsRelayHoldDuration = 5 * time.Minute

	// directVsRelayThresholdPercent is the default Phase 20-style relative
	// gate applied to cross-category swaps: the alternative category must
	// be at least this many percent faster (lower mean RTT) than the
	// current category before the swap fires. Same default as Phase 20's
	// sourcePathAuxBeatThresholdPercent for consistency, but is its own
	// independent env knob so operators can tune the two gates separately.
	directVsRelayThresholdPercent = 10
)

// directVsRelayCompareEnabled reports whether the Phase 22 v2 direct-vs-
// relay comparison is opted in.
func directVsRelayCompareEnabled() bool {
	return envknobDirectVsRelayCompare()
}

// directVsRelayCompareIntervalValue returns the configured comparison
// interval, or the default if unset / negative.
func directVsRelayCompareIntervalValue() time.Duration {
	n := envknobDirectVsRelayCompareInterv()
	if n <= 0 {
		return directVsRelayCompareInterval
	}
	return time.Duration(n) * time.Second
}

// directVsRelayHoldDurationValue returns the configured per-peer
// hysteresis window, or the default if unset / negative.
func directVsRelayHoldDurationValue() time.Duration {
	n := envknobDirectVsRelayHold()
	if n <= 0 {
		return directVsRelayHoldDuration
	}
	return time.Duration(n) * time.Second
}

// directVsRelayThresholdValue returns the cross-category swap gate
// percent, clamped to [1, 100]. A value of 0 (env unset) maps to the
// default directVsRelayThresholdPercent. A negative env value disables
// the comparison gate entirely (returns 0; callers treat 0 as "skip the
// gate"). 100 means "no swap is ever allowed".
func directVsRelayThresholdValue() int {
	n := envknobDirectVsRelayThresholdPct()
	if n < 0 {
		return 0
	}
	if n == 0 {
		return directVsRelayThresholdPercent
	}
	if n > 100 {
		return 100
	}
	return n
}

// directBeatsRelayByThresholdLocked reports whether `direct` is at least
// the threshold-percent faster than `relay` (i.e. relay should NOT
// preempt direct). Both arguments are mean RTTs over the relevant sample
// window. Returns false when either latency is non-positive (insufficient
// signal to decide), so the caller's default (existing direct preference)
// stands.
//
// Used by betterAddr() to gate the cross-category direct↔relay swap when
// directVsRelayCompareEnabled() is true.
func directBeatsRelayByThresholdLocked(directLat, relayLat time.Duration, thresholdPct int) bool {
	if directLat <= 0 || relayLat <= 0 {
		return false
	}
	if thresholdPct <= 0 {
		return false
	}
	if thresholdPct > 100 {
		thresholdPct = 100
	}
	// "direct beats relay by ≥ thresholdPct" means direct's RTT is at
	// least thresholdPct% lower than relay's (i.e. direct < relay × (1 -
	// thresholdPct/100)). Equivalent integer form to avoid float math:
	//   direct * 100 < relay * (100 - thresholdPct)
	return int64(directLat)*100 < int64(relayLat)*int64(100-thresholdPct)
}

// relayBeatsDirectByThresholdLocked is the symmetric check used to decide
// whether to swap from direct→relay.
func relayBeatsDirectByThresholdLocked(directLat, relayLat time.Duration, thresholdPct int) bool {
	return directBeatsRelayByThresholdLocked(relayLat, directLat, thresholdPct)
}
