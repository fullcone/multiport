// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import "testing"

func TestPrimarySourceRxMeta(t *testing.T) {
	if primarySourceRxMeta.socketID != primarySourceSocketID {
		t.Fatalf("primary source metadata uses socket ID %d, want %d", primarySourceRxMeta.socketID, primarySourceSocketID)
	}
}
