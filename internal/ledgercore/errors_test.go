package ledgercore

import (
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestErrors_Sentinels(t *testing.T) {
	sentinels := []error{
		ErrNotFound,
		ErrConflict,
		ErrDuplicate,
		ErrClaimHeld,
		ErrClaimNotHeld,
		ErrClosed,
		ErrInvalidTransition,
		ErrContentNotFound,
	}
	for _, err := range sentinels {
		if err == nil || err.Error() == "" {
			t.Errorf("expected non-empty error, got %v", err)
		}
	}
}

func TestMapStorageError(t *testing.T) {
	cases := []struct {
		in   error
		want error
	}{
		{storage.ErrClaimHeld, ErrClaimHeld},
		{storage.ErrClaimNotHeld, ErrClaimNotHeld},
		{storage.ErrDuplicate, ErrDuplicate},
		{storage.ErrContentNotFound, ErrContentNotFound},
		{errors.New("other error"), nil},
		{nil, nil},
	}

	for _, tc := range cases {
		got := MapStorageError(tc.in)
		if tc.want != nil {
			if !errors.Is(got, tc.want) {
				t.Errorf("MapStorageError(%v) = %v; want %v", tc.in, got, tc.want)
			}
		} else if tc.in != nil && got != tc.in {
			t.Errorf("MapStorageError(%v) = %v; want verbatim %v", tc.in, got, tc.in)
		} else if tc.in == nil && got != nil {
			t.Errorf("MapStorageError(nil) = %v; want nil", got)
		}
	}
}
