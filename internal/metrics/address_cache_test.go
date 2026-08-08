package metrics

import "testing"

func TestAddressCacheRefusesAmbiguousTruncatedExpansion(t *testing.T) {
	const first = "0xabcd111111111111111111111111111111111234"
	const second = "0xabcd222222222222222222222222222222221234"
	const truncated = "0xabcd..1234"

	ClearAddressCache()
	t.Cleanup(ClearAddressCache)
	RegisterFullAddress(first)
	if got := ExpandAddress(truncated); got != first {
		t.Fatalf("unique expansion = %q, want %q", got, first)
	}
	RegisterFullAddress(second)
	if got := ExpandAddress(truncated); got != truncated {
		t.Fatalf("ambiguous expansion = %q, want unresolved %q", got, truncated)
	}
	if size := GetAddressCacheSize(); size != 0 {
		t.Fatalf("ambiguous cache retained %d resolvable entries, want 0", size)
	}

	// Re-registering either candidate cannot make an ambiguous identity unique
	// without replacing the complete address generation.
	RegisterFullAddress(first)
	if got := ExpandAddress(truncated); got != truncated {
		t.Fatalf("ambiguous expansion revived as %q", got)
	}
}
