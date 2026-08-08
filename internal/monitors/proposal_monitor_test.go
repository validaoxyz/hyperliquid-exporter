package monitors

import (
	"testing"
	"time"
)

func TestParseProposalLineAcceptsCompleteBlock(t *testing.T) {
	observation, proposal, err := parseProposalLine(`{"height":1,"abci_block":{"time":"2026-08-08T03:16:34.462864575","proposer":"0xabc"}}`)
	if err != nil {
		t.Fatalf("parse proposal: %v", err)
	}
	if !proposal {
		t.Fatal("complete abci_block was not classified as a proposal")
	}
	want := time.Date(2026, 8, 8, 3, 16, 34, 462864575, time.UTC)
	if !observation.sourceTime.Equal(want) {
		t.Fatalf("source time = %s, want %s", observation.sourceTime, want)
	}
	if observation.proposer != "0xabc" {
		t.Fatalf("proposer = %q", observation.proposer)
	}
}

func TestParseProposalLineRejectsNullRequiredFields(t *testing.T) {
	for name, line := range map[string]string{
		"block":    `{"abci_block":null}`,
		"proposer": `{"abci_block":{"time":"2026-08-08T03:16:34Z","proposer":null}}`,
		"time":     `{"abci_block":{"time":null,"proposer":"0xabc"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, proposal, err := parseProposalLine(line); !proposal || err == nil {
				t.Fatalf("proposal=%v err=%v, want claimed malformed proposal", proposal, err)
			}
		})
	}
}

func TestParseProposalLineIgnoresNonProposalNoise(t *testing.T) {
	for _, line := range []string{"diagnostic text", `{"kind":"other"}`, ""} {
		if _, proposal, err := parseProposalLine(line); err != nil || proposal {
			t.Fatalf("line %q: proposal=%v err=%v", line, proposal, err)
		}
	}
}
