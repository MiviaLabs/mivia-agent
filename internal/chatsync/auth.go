package chatsync

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// ErrAuthStop reports an authentication failure that no retry can fix: the
// local CLI session is gone and only `mivia login` restores it. Sync stops on
// this error instead of retrying, because the alternative is a background
// loop that either prompts in the middle of a chat or hammers the API with a
// dead session.
var ErrAuthStop = errors.New("chatsync: sync stopped; run `mivia login`")

// TokenEnsurer is the narrow view of *miviaauth.Service this package needs.
//
// It is an interface rather than the concrete Service so tests can drive the
// busy and reauth branches without a token file, but it is deliberately NOT a
// second token cache: the refresh token is one-time use and
// internal/miviaauth/lock.go serializes the refresh window across processes.
// Every caller must hand over the one real Service so that lock stays the
// only refresher.
type TokenEnsurer interface {
	Ensure(ctx context.Context) (string, error)
}

// NewTokenProvider adapts a TokenEnsurer to a TokenProvider, and classifies
// its errors per the settled 401 policy:
//
//   - ErrRefreshBusy is TRANSIENT. Another mivia process holds the refresh
//     lock; the stored token is untouched and the next flush retries.
//   - ErrReauthRequired and ErrSessionLost STOP sync. There is no local
//     session left to refresh, and this path must never prompt mid-chat.
//
// forceRefresh is ignored on purpose. Ensure already owns the refresh
// decision, under the cross-process lock; refreshing again from here would be
// exactly the double-refresh that revokes the session for both processes.
func NewTokenProvider(ensurer TokenEnsurer) TokenProvider {
	if ensurer == nil {
		return nil
	}
	return func(ctx context.Context, _ bool) (string, error) {
		tok, err := ensurer.Ensure(ctx)
		if err == nil {
			return tok, nil
		}
		if errors.Is(err, miviaauth.ErrReauthRequired) || errors.Is(err, miviaauth.ErrSessionLost) {
			return "", fmt.Errorf("%w: %w", ErrAuthStop, err)
		}
		return "", fmt.Errorf("chatsync: no usable token: %w", err)
	}
}
