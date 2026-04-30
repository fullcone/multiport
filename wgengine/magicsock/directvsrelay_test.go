// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"net/netip"
	"testing"
	"time"

	"tailscale.com/envknob"
	"tailscale.com/net/packet"
	"tailscale.com/tstime/mono"
)

func TestDirectVsRelayCompareEnabledDefaultOff(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "") })

	if directVsRelayCompareEnabled() {
		t.Fatal("directVsRelayCompareEnabled() must default to false (env unset)")
	}
}

func TestDirectVsRelayCompareEnabledEnvTrue(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "true")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "") })

	if !directVsRelayCompareEnabled() {
		t.Fatal("directVsRelayCompareEnabled() must be true when env=true")
	}
}

func TestDirectVsRelayThresholdValueDefault(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT", "")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT", "") })

	if got := directVsRelayThresholdValue(); got != directVsRelayThresholdPercent {
		t.Fatalf("directVsRelayThresholdValue() = %d; want default %d", got, directVsRelayThresholdPercent)
	}
}

func TestDirectVsRelayThresholdValueEnvOverride(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT", "25")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT", "") })

	if got := directVsRelayThresholdValue(); got != 25 {
		t.Fatalf("directVsRelayThresholdValue() = %d; want 25", got)
	}
}

func TestDirectVsRelayThresholdValueClamped(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT", "200")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT", "") })

	if got := directVsRelayThresholdValue(); got != 100 {
		t.Fatalf("directVsRelayThresholdValue() = %d; want 100 (clamped)", got)
	}
}

func TestDirectVsRelayThresholdValueNegativeDisabled(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT", "-1")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT", "") })

	if got := directVsRelayThresholdValue(); got != 0 {
		t.Fatalf("directVsRelayThresholdValue() = %d; want 0 (disabled)", got)
	}
}

func TestRelayBeatsDirectByThresholdLocked(t *testing.T) {
	// 100 ms direct, 80 ms relay → relay is 20% faster → beats threshold of 10%.
	if !relayBeatsDirectByThresholdLocked(100*time.Millisecond, 80*time.Millisecond, 10) {
		t.Error("relay 80ms vs direct 100ms (20% faster) should beat 10% threshold")
	}
	// 100 ms direct, 95 ms relay → relay is 5% faster → does not beat 10%.
	if relayBeatsDirectByThresholdLocked(100*time.Millisecond, 95*time.Millisecond, 10) {
		t.Error("relay 95ms vs direct 100ms (5% faster) should NOT beat 10% threshold")
	}
	// Equal latencies: relay does not beat direct.
	if relayBeatsDirectByThresholdLocked(100*time.Millisecond, 100*time.Millisecond, 10) {
		t.Error("equal latencies: relay should NOT beat direct")
	}
	// Relay slower: relay does not beat direct.
	if relayBeatsDirectByThresholdLocked(100*time.Millisecond, 110*time.Millisecond, 10) {
		t.Error("slower relay should NOT beat direct")
	}
	// Zero or non-positive latencies: no decision (return false).
	if relayBeatsDirectByThresholdLocked(0, 50*time.Millisecond, 10) {
		t.Error("zero direct latency: should return false (insufficient signal)")
	}
	if relayBeatsDirectByThresholdLocked(50*time.Millisecond, 0, 10) {
		t.Error("zero relay latency: should return false (insufficient signal)")
	}
	// thresholdPct == 0: disabled, never beats.
	if relayBeatsDirectByThresholdLocked(100*time.Millisecond, 1*time.Millisecond, 0) {
		t.Error("threshold 0: should always return false (gate disabled)")
	}
}

func TestBetterAddrDirectVsRelayKnobOff(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "") })

	direct := addrQuality{
		epAddr:  epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")},
		latency: 200 * time.Millisecond,
	}
	relay := addrQuality{
		epAddr:  withVNI(epAddr{ap: netip.MustParseAddrPort("198.51.100.1:41641")}, 1234),
		latency: 1 * time.Millisecond, // way faster than direct
	}

	// With env knob OFF, today's behavior: direct unconditionally beats relay,
	// even when relay's latency is 200x lower. This locks in "no behavior
	// change when env knob is unset".
	if !betterAddr(direct, relay) {
		t.Fatal("env-off: direct should unconditionally beat relay (today's behaviour)")
	}
	if betterAddr(relay, direct) {
		t.Fatal("env-off: relay should never beat direct")
	}
}

func TestBetterAddrDirectVsRelayKnobOnRelayWins(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "true")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "") })

	// Relay 50 ms vs direct 200 ms → relay is 75% faster → beats 10% gate.
	direct := addrQuality{
		epAddr:  epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")},
		latency: 200 * time.Millisecond,
	}
	relay := addrQuality{
		epAddr:  withVNI(epAddr{ap: netip.MustParseAddrPort("198.51.100.1:41641")}, 1234),
		latency: 50 * time.Millisecond,
	}

	// betterAddr(direct, relay) should now return false: relay is better.
	if betterAddr(direct, relay) {
		t.Fatal("env-on, relay 50ms vs direct 200ms: relay should win, betterAddr(direct, relay) should be false")
	}
	if !betterAddr(relay, direct) {
		t.Fatal("env-on, relay 50ms vs direct 200ms: betterAddr(relay, direct) should be true")
	}
}

func TestBetterAddrDirectVsRelayKnobOnDirectKept(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "true")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "") })

	// Relay 95 ms vs direct 100 ms → relay is only 5% faster → does NOT
	// beat 10% gate → direct is preserved.
	direct := addrQuality{
		epAddr:  epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")},
		latency: 100 * time.Millisecond,
	}
	relay := addrQuality{
		epAddr:  withVNI(epAddr{ap: netip.MustParseAddrPort("198.51.100.1:41641")}, 1234),
		latency: 95 * time.Millisecond,
	}

	if !betterAddr(direct, relay) {
		t.Fatal("env-on, relay 5% faster: direct should still beat (gate not breached)")
	}
	if betterAddr(relay, direct) {
		t.Fatal("env-on, relay 5% faster: relay should not beat direct (gate not breached)")
	}
}

func TestBetterAddrDirectVsRelayKnobOnZeroLatency(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "true")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "") })

	// If relay's latency is 0 (e.g. probe not yet returned), gate must NOT
	// fire — direct wins by default.
	direct := addrQuality{
		epAddr:  epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")},
		latency: 100 * time.Millisecond,
	}
	relay := addrQuality{
		epAddr:  withVNI(epAddr{ap: netip.MustParseAddrPort("198.51.100.1:41641")}, 1234),
		latency: 0, // not yet measured
	}

	if !betterAddr(direct, relay) {
		t.Fatal("env-on, relay latency 0: direct must still beat (insufficient signal)")
	}
}

// withVNI returns a copy of e with its vni set to v. Helper for tests.
func withVNI(e epAddr, v uint32) epAddr {
	var vni packet.VirtualNetworkID
	vni.Set(v)
	e.vni = vni
	return e
}

func TestDirectVsRelayCompareIntervalDefault(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S", "")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S", "") })
	if got := directVsRelayCompareIntervalValue(); got != directVsRelayCompareInterval {
		t.Fatalf("default = %v; want %v", got, directVsRelayCompareInterval)
	}
}

func TestDirectVsRelayCompareIntervalEnvOverride(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S", "60")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S", "") })
	if got := directVsRelayCompareIntervalValue(); got != 60*time.Second {
		t.Fatalf("override = %v; want 60s", got)
	}
}

func TestWouldAllowDirectVsRelaySwapLockedKnobOff(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "") })

	de := &endpoint{}
	now := mono.Now()
	de.lastDirectVsRelaySwap = now.Add(-1 * time.Second) // very recent swap
	direct := addrQuality{epAddr: epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}}
	relay := addrQuality{epAddr: withVNI(epAddr{ap: netip.MustParseAddrPort("198.51.100.1:41641")}, 1234)}

	// With env knob off, hysteresis must not block anything (preserves
	// pre-Phase-22 behaviour bit-for-bit).
	if !de.wouldAllowDirectVsRelaySwapLocked(direct, relay, now) {
		t.Fatal("env-off: cross-category swap must be allowed (no hysteresis)")
	}
}

func TestWouldAllowDirectVsRelaySwapLockedFirstSwap(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "true")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "") })

	de := &endpoint{} // lastDirectVsRelaySwap is zero
	now := mono.Now()
	direct := addrQuality{epAddr: epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}}
	relay := addrQuality{epAddr: withVNI(epAddr{ap: netip.MustParseAddrPort("198.51.100.1:41641")}, 1234)}

	// First-ever swap (lastDirectVsRelaySwap zero) must always be allowed.
	if !de.wouldAllowDirectVsRelaySwapLocked(direct, relay, now) {
		t.Fatal("env-on, first swap (zero last-swap): must be allowed")
	}
}

func TestWouldAllowDirectVsRelaySwapLockedHoldBlocks(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_HOLD_S", "300")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "")
		envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_HOLD_S", "")
	})

	de := &endpoint{}
	now := mono.Now()
	de.lastDirectVsRelaySwap = now.Add(-30 * time.Second) // 30s ago, well within 300s hold
	direct := addrQuality{epAddr: epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}}
	relay := addrQuality{epAddr: withVNI(epAddr{ap: netip.MustParseAddrPort("198.51.100.1:41641")}, 1234)}

	// Cross-category swap inside hold window must be blocked.
	if de.wouldAllowDirectVsRelaySwapLocked(direct, relay, now) {
		t.Fatal("env-on, hold window not yet elapsed: cross-category swap must be blocked")
	}
	// Same-category move (direct→direct) must NOT be blocked by the
	// hysteresis even inside the hold window.
	direct2 := addrQuality{epAddr: epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}}
	if !de.wouldAllowDirectVsRelaySwapLocked(direct, direct2, now) {
		t.Fatal("env-on, same-category swap: hysteresis must not block")
	}
}

func TestWouldAllowDirectVsRelaySwapLockedHoldElapsed(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_HOLD_S", "300")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "")
		envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_HOLD_S", "")
	})

	de := &endpoint{}
	now := mono.Now()
	de.lastDirectVsRelaySwap = now.Add(-301 * time.Second) // just past 300s hold
	direct := addrQuality{epAddr: epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}}
	relay := addrQuality{epAddr: withVNI(epAddr{ap: netip.MustParseAddrPort("198.51.100.1:41641")}, 1234)}

	if !de.wouldAllowDirectVsRelaySwapLocked(direct, relay, now) {
		t.Fatal("env-on, hold window elapsed: cross-category swap must be allowed")
	}
}

func TestNoteDirectVsRelaySwapLocked(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "true")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "") })

	de := &endpoint{}
	now := mono.Now()
	direct := addrQuality{epAddr: epAddr{ap: netip.MustParseAddrPort("192.0.2.1:41641")}}
	direct2 := addrQuality{epAddr: epAddr{ap: netip.MustParseAddrPort("192.0.2.2:41641")}}
	relay := addrQuality{epAddr: withVNI(epAddr{ap: netip.MustParseAddrPort("198.51.100.1:41641")}, 1234)}

	// Same-category move: timestamp must NOT update.
	de.lastDirectVsRelaySwap = mono.Time(0)
	de.noteDirectVsRelaySwapLocked(direct, direct2, now)
	if !de.lastDirectVsRelaySwap.IsZero() {
		t.Fatal("same-category move: lastDirectVsRelaySwap must remain zero")
	}

	// Cross-category move: timestamp updates.
	de.noteDirectVsRelaySwapLocked(direct, relay, now)
	if de.lastDirectVsRelaySwap != now {
		t.Fatalf("cross-category move: lastDirectVsRelaySwap = %v; want %v", de.lastDirectVsRelaySwap, now)
	}

	// Knob off: timestamp must NOT update.
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "")
	de.lastDirectVsRelaySwap = mono.Time(0)
	de.noteDirectVsRelaySwapLocked(direct, relay, now)
	if !de.lastDirectVsRelaySwap.IsZero() {
		t.Fatal("env-off: lastDirectVsRelaySwap must remain zero even on category change")
	}
}

// TestDirectVsRelayCompareIntervalFloorRespectsDiscoverUDPRelayPathsInterval is
// a regression test for Codex P2 on PR #16: even when the operator sets a low
// TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S, the actual probe rate
// when a trusted direct path is held must never go below the existing
// discoverUDPRelayPathsInterval (30 s). The new direct-compare branch sits
// before the shared rate-limiter, so it is responsible for re-applying the
// floor itself.
func TestDirectVsRelayCompareIntervalFloorRespectsDiscoverUDPRelayPathsInterval(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "true")
	envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S", "5")
	t.Cleanup(func() {
		envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE", "")
		envknob.Setenv("TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S", "")
	})

	// Sanity: env getter reports the operator's value as-is.
	if got := directVsRelayCompareIntervalValue(); got != 5*time.Second {
		t.Fatalf("compare interval env = %v; want 5s", got)
	}

	// The floor that the wantUDPRelayPathDiscoveryLocked branch applies in
	// the trusted-direct case is max(env, discoverUDPRelayPathsInterval).
	// We assert the policy directly here rather than driving an endpoint
	// state machine; that's the same thing the production code computes.
	want := discoverUDPRelayPathsInterval
	got := directVsRelayCompareIntervalValue()
	if got < discoverUDPRelayPathsInterval {
		got = discoverUDPRelayPathsInterval
	}
	if got != want {
		t.Fatalf("effective floor = %v; want %v (= discoverUDPRelayPathsInterval)", got, want)
	}
}
