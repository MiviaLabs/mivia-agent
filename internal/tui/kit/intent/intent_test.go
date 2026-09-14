package intent

import "testing"

func TestSend(t *testing.T) {
	s := Send{Text: "hello"}
	if s.Text != "hello" {
		t.Errorf("Send.Text = %q, want %q", s.Text, "hello")
	}
}
