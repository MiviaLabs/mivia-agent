package miviaauth

import (
	"testing"
	"time"
)

func TestTokenExpired(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		expiresAt time.Time
		now       time.Time
		want      bool
	}{
		{"well before expiry", now.Add(time.Hour), now, false},
		{"exactly at expiry", now, now, true},
		{"after expiry", now.Add(-time.Second), now, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok := Token{ExpiresAt: tt.expiresAt}
			if got := tok.Expired(tt.now); got != tt.want {
				t.Fatalf("Expired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTokenNeedsRefresh(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	skew := 5 * time.Minute
	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{"plenty of time left", now.Add(time.Hour), false},
		{"just outside skew window", now.Add(skew + time.Second), false},
		{"exactly at skew boundary", now.Add(skew), true},
		{"within skew window", now.Add(time.Minute), true},
		{"already expired", now.Add(-time.Minute), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok := Token{ExpiresAt: tt.expiresAt}
			if got := tok.NeedsRefresh(now, skew); got != tt.want {
				t.Fatalf("NeedsRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}
