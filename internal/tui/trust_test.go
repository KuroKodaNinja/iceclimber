package tui

import (
	"bytes"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

func runTrustPrompt(t *testing.T, info HostKeyInfo, key rune) TrustPrompt {
	t.Helper()
	tm := teatest.NewTestModel(t, NewTrustPrompt(info), teatest.WithInitialTermSize(80, 24))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("fingerprint:"))
	}, teatest.WithDuration(3*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
	return tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(TrustPrompt)
}

// Pressing y records the operator's accept; the fingerprint is shown first.
func TestTrustPrompt_Accept(t *testing.T) {
	info := HostKeyInfo{SandboxID: "sbx", Address: "127.0.0.1:2222", KeyType: "ssh-ed25519", Fingerprint: "SHA256:abc123"}
	final := runTrustPrompt(t, info, 'y')
	if !final.Accepted() {
		t.Error("pressing y did not accept")
	}
}

// Pressing n declines — the security floor is never lowered without an explicit yes.
func TestTrustPrompt_Decline(t *testing.T) {
	info := HostKeyInfo{SandboxID: "sbx", Address: "127.0.0.1:2222", KeyType: "ssh-ed25519", Fingerprint: "SHA256:abc123"}
	final := runTrustPrompt(t, info, 'n')
	if final.Accepted() {
		t.Error("pressing n accepted")
	}
}

// A changed key renders the mismatch warning prominently.
func TestTrustPrompt_MismatchWarns(t *testing.T) {
	info := HostKeyInfo{SandboxID: "sbx", Address: "127.0.0.1:2222", KeyType: "ssh-ed25519", Fingerprint: "SHA256:abc123", Mismatch: true}
	tm := teatest.NewTestModel(t, NewTrustPrompt(info), teatest.WithInitialTermSize(80, 24))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("CHANGED")) && bytes.Contains(b, []byte("man-in-the-middle"))
	}, teatest.WithDuration(3*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}
