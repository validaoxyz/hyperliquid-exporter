package config

import (
	"path/filepath"
	"testing"
)

func TestNormalizeChain(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "mainnet", raw: "mainnet", want: "mainnet"},
		{name: "case and whitespace", raw: "  TestNet \n", want: "testnet"},
		{name: "empty", raw: "", wantErr: true},
		{name: "whitespace", raw: "  ", wantErr: true},
		{name: "typo", raw: "test-net", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeChain(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeChain(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("NormalizeChain(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestLoadConfigDerivesPathsAfterPrecedence(t *testing.T) {
	tests := []struct {
		name         string
		envNodeHome  string
		flagNodeHome string
		wantNodeHome string
	}{
		{name: "default", wantNodeHome: "/home/operator/hl"},
		{name: "environment", envNodeHome: "/env/hl", wantNodeHome: "/env/hl"},
		{name: "flag", envNodeHome: "/env/hl", flagNodeHome: "/flag/hl", wantNodeHome: "/flag/hl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", "/home/operator")
			t.Setenv("NODE_HOME", tt.envNodeHome)
			t.Setenv("BINARY_HOME", "/binary/home")
			t.Setenv("NODE_BINARY", "")

			cfg, err := LoadConfig(&Flags{Chain: "testnet", NodeHome: tt.flagNodeHome})
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if cfg.NodeHome != tt.wantNodeHome {
				t.Fatalf("NodeHome = %q, want %q", cfg.NodeHome, tt.wantNodeHome)
			}
			if want := filepath.Join(tt.wantNodeHome, "data", "replica_cmds"); cfg.ReplicaDataDir != want {
				t.Fatalf("ReplicaDataDir = %q, want %q", cfg.ReplicaDataDir, want)
			}
			if cfg.BinaryHome != "/binary/home" || cfg.NodeBinary != "/binary/home/hl-node" {
				t.Fatalf("binary paths = (%q, %q), want env home-derived binary", cfg.BinaryHome, cfg.NodeBinary)
			}
		})
	}
}

func TestLoadConfigNodeBinaryFlagWins(t *testing.T) {
	t.Setenv("HOME", "/home/operator")
	t.Setenv("NODE_HOME", "")
	t.Setenv("BINARY_HOME", "/env/bin")
	t.Setenv("NODE_BINARY", "/env/bin/custom-node")

	cfg, err := LoadConfig(&Flags{Chain: "mainnet", NodeBinary: "/flag/hl-node"})
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.NodeBinary != "/flag/hl-node" {
		t.Fatalf("NodeBinary = %q, want flag value", cfg.NodeBinary)
	}
}

func TestLoadConfigPreservesEVMBlockTypeFlag(t *testing.T) {
	t.Setenv("HOME", "/home/operator")
	t.Setenv("NODE_HOME", "")
	t.Setenv("BINARY_HOME", "")
	t.Setenv("NODE_BINARY", "")

	for _, enableEVM := range []bool{false, true} {
		for _, enableBlockType := range []bool{false, true} {
			name := "evm_false_block_false"
			if enableEVM {
				name = "evm_true_block_false"
			}
			if enableBlockType {
				name += "_block_true"
			}
			t.Run(name, func(t *testing.T) {
				cfg, err := LoadConfig(&Flags{
					Chain:               "testnet",
					EnableEVM:           enableEVM,
					EVMBlockTypeMetrics: enableBlockType,
				})
				if err != nil {
					t.Fatalf("LoadConfig() error = %v", err)
				}
				if cfg.EnableEVM != enableEVM || cfg.EVMBlockTypeMetrics != enableBlockType {
					t.Fatalf("got EVM=%v blockType=%v, want %v/%v",
						cfg.EnableEVM, cfg.EVMBlockTypeMetrics, enableEVM, enableBlockType)
				}
			})
		}
	}
}

func TestLoadConfigRejectsMissingOrUnknownChain(t *testing.T) {
	t.Setenv("HOME", "/home/operator")
	for _, flags := range []*Flags{nil, {Chain: ""}, {Chain: "devnet"}} {
		if _, err := LoadConfig(flags); err == nil {
			t.Fatalf("LoadConfig(%#v) unexpectedly succeeded", flags)
		}
	}
}

func TestParseTCPServicePorts(t *testing.T) {
	got, err := ParseTCPServicePorts(" 4004,3001,3999 ")
	if err != nil {
		t.Fatalf("ParseTCPServicePorts(valid) error = %v", err)
	}
	want := []uint16{3001, 3999, 4004}
	if !equalPorts(got, want) {
		t.Fatalf("ports = %v, want sorted %v", got, want)
	}

	tooMany := "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17"
	for _, raw := range []string{
		"",
		"3001,",
		"3001,,4001",
		"three-thousand",
		"+3001",
		"0",
		"65536",
		"3001,3001",
		tooMany,
	} {
		if _, err := ParseTCPServicePorts(raw); err == nil {
			t.Fatalf("ParseTCPServicePorts(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestLoadConfigTCPDefaultsAndOverrides(t *testing.T) {
	t.Setenv("HOME", "/home/operator")
	t.Setenv("NODE_HOME", "")
	t.Setenv("BINARY_HOME", "")
	t.Setenv("NODE_BINARY", "")

	defaults, err := LoadConfig(&Flags{Chain: "testnet"})
	if err != nil {
		t.Fatalf("LoadConfig(default TCP) error = %v", err)
	}
	wantDefault := []uint16{3001, 3999, 4001, 4002, 4003, 4004}
	if !equalPorts(defaults.TCPServicePorts, wantDefault) || !defaults.EnableTCP6 {
		t.Fatalf("default TCP config = ports %v enableTCP6=%v, want %v/true",
			defaults.TCPServicePorts, defaults.EnableTCP6, wantDefault)
	}

	override, err := LoadConfig(&Flags{
		Chain:           "mainnet",
		TCPServicePorts: "5000, 4001",
		DisableTCP6:     true,
	})
	if err != nil {
		t.Fatalf("LoadConfig(TCP override) error = %v", err)
	}
	if !equalPorts(override.TCPServicePorts, []uint16{4001, 5000}) || override.EnableTCP6 {
		t.Fatalf("override TCP config = ports %v enableTCP6=%v, want [4001 5000]/false",
			override.TCPServicePorts, override.EnableTCP6)
	}
	if _, err := LoadConfig(&Flags{Chain: "testnet", TCPServicePorts: "4001,4001"}); err == nil {
		t.Fatal("LoadConfig accepted duplicate TCP service ports")
	}
}

func equalPorts(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
