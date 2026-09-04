package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRealtimeKitCreatesMeetingAndParticipantWithoutExposingAPIToken(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer server-secret" {
			t.Fatalf("missing server authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		switch requests {
		case 1:
			if r.URL.Path != "/accounts/account/realtime/kit/app/meetings" {
				t.Fatalf("unexpected meeting path: %s", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"meeting-id"}}`))
		case 2:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["custom_participant_id"] != "user:session" || body["preset_name"] != "speaker" {
				t.Fatalf("unexpected participant body: %+v", body)
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"participant-id","token":"client-token"}}`))
		case 3:
			if r.Method != http.MethodDelete {
				t.Fatalf("expected participant delete")
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	provider, err := NewRealtimeKit(RealtimeKitConfig{AccountID: "account", AppID: "app", APIToken: "server-secret", ListenerPresetName: "listener", VoicePresetName: "speaker", StreamPresetName: "streamer", APIBase: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	room, err := provider.CreateRoom(context.Background(), "Aster room")
	if err != nil || room != "meeting-id" {
		t.Fatalf("unexpected room: %q %v", room, err)
	}
	participant, err := provider.AddParticipant(context.Background(), room, "user:session", "Alice", true, false)
	if err != nil || participant.ID != "participant-id" || participant.Credential != "client-token" {
		t.Fatalf("unexpected participant: %+v %v", participant, err)
	}
	if err := provider.RemoveParticipant(context.Background(), room, participant.ID); err != nil {
		t.Fatal(err)
	}
}
