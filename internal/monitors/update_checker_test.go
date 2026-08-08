package monitors

import "testing"

func TestVisorURLForChainFailsClosed(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: " mainNET ", want: "https://binaries.hyperliquid.xyz/Mainnet/hl-visor"},
		{raw: "TESTNET", want: "https://binaries.hyperliquid-testnet.xyz/Testnet/hl-visor"},
		{raw: "", wantErr: true},
		{raw: "devnet", wantErr: true},
	}
	for _, tt := range tests {
		got, err := visorURLForChain(tt.raw)
		if (err != nil) != tt.wantErr {
			t.Fatalf("visorURLForChain(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
		}
		if got != tt.want {
			t.Fatalf("visorURLForChain(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}
