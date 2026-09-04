package localvoice

import (
	"context"
	"testing"
)

func TestProviderCreatesOpaqueLocalSession(t *testing.T) {
	provider := New()
	roomID, err := provider.CreateRoom(context.Background(), "Local test")
	if err != nil || roomID == "" {
		t.Fatalf("unexpected room: %q %v", roomID, err)
	}
	participant, err := provider.AddParticipant(context.Background(), roomID, "user:session", "Alice", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if participant.ID == "" || len(participant.Credential) < 32 || participant.ExpiresAt.IsZero() {
		t.Fatalf("unexpected participant: %+v", participant)
	}
	if provider.Name() != "aster-local" || provider.Endpoint() != "aster-local://media" {
		t.Fatalf("unexpected provider identity: %s %s", provider.Name(), provider.Endpoint())
	}
}
