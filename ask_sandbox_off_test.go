package caretz

import "testing"

// The interactive app runs every session through this client, so its
// registry is the one that must escalate sandbox refusals to the user.
func TestClientRegistryEscalatesSandboxRefusals(t *testing.T) {
	client, err := NewClient(t.Context(), ClientOptions{WorkspaceDir: t.TempDir(), Sandbox: SandboxOptions{AskSandboxOff: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !client.toolRegistry.AskSandboxOff || client.toolRegistry.RunUnsandboxed == nil {
		t.Fatalf("client registry cannot escalate: ask=%v runner=%v", client.toolRegistry.AskSandboxOff, client.toolRegistry.RunUnsandboxed != nil)
	}
	plain, err := NewClient(t.Context(), ClientOptions{WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if plain.toolRegistry.AskSandboxOff {
		t.Fatal("escalation enabled without being requested")
	}
}
