// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package envknob

import "testing"

func TestSetenvUpdatesRegisteredInt(t *testing.T) {
	const name = "TS_TEST_SETENV_UPDATES_REGISTERED_INT"
	t.Cleanup(func() {
		Setenv(name, "")
	})

	get := RegisterInt(name)
	if got := get(); got != 0 {
		t.Fatalf("initial registered int = %d, want 0", got)
	}

	Setenv(name, "7")
	if got := get(); got != 7 {
		t.Fatalf("registered int after Setenv = %d, want 7", got)
	}

	Setenv(name, "")
	if got := get(); got != 0 {
		t.Fatalf("registered int after clearing env = %d, want 0", got)
	}
}
