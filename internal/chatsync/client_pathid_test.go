package chatsync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestValidatePathID pins the allowlist directly: this is the guard every
// path-interpolating Client method calls before a server-or-caller-supplied
// id reaches fmt.Sprintf.
func TestValidatePathID(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"plain", "sess-abc_123.1", false},
		{"empty", "", true},
		{"path traversal", "../../etc/passwd", true},
		{"embedded slash", "sess/1", true},
		{"query injection", "sess-1?x=1", true},
		{"fragment", "sess-1#frag", true},
		{"crlf header injection", "sess-1\r\nX-Evil: 1", true},
		{"whitespace", "sess 1", true},
		{"too long", string(make([]byte, 257)), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePathID(tc.id)
			if tc.wantErr && err == nil {
				t.Fatalf("validatePathID(%q) = nil, want error", tc.id)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validatePathID(%q) = %v, want nil", tc.id, err)
			}
			if tc.wantErr && err != nil && !errors.Is(err, ErrInvalidPathID) {
				t.Errorf("validatePathID(%q) error = %v, want wrapping ErrInvalidPathID", tc.id, err)
			}
		})
	}
}

// TestConsumeInput_RejectsHostileInputID drives the client with a hostile
// fake server: NextInput hands back an id shaped like a path-traversal
// attempt. ConsumeInput re-embeds that id into a second request's path
// (poller.go's pollOnce does exactly this with the server's own response),
// so it must refuse before a single byte of that id reaches net/http.
func TestConsumeInput_RejectsHostileInputID(t *testing.T) {
	var consumeHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		consumeHits++
		http.Error(w, "should never be reached", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})

	hostileIDs := []string{
		"../sessions/other-session/end",
		"inp-1/../../admin",
		"inp-1\r\nX-Injected: 1",
		"inp 1",
	}
	for _, id := range hostileIDs {
		if _, err := client.ConsumeInput(context.Background(), "sess-1", id); err == nil {
			t.Errorf("ConsumeInput(sessionID=%q) error = nil, want refusal", id)
		}
	}
	if consumeHits != 0 {
		t.Errorf("server saw %d request(s) for a rejected id, want 0", consumeHits)
	}
}

// TestNextInput_RejectsHostileSessionID guards the sibling call: sessionID
// reaches NextInput from the persisted identity / attach response, the same
// trust boundary as ConsumeInput's inputID (AGENTS.md's "one invariant,
// several sites" class).
func TestNextInput_RejectsHostileSessionID(t *testing.T) {
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "should never be reached", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	if _, err := client.NextInput(context.Background(), "../admin", 1); err == nil {
		t.Fatal("NextInput(hostile sessionID) error = nil, want refusal")
	}
	if hits != 0 {
		t.Errorf("server saw %d request(s) for a rejected id, want 0", hits)
	}
}
