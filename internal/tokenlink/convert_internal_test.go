package tokenlink

import (
	"testing"
	"time"

	"github.com/tamcore/garmin-mcp/internal/garmin/auth"
	"github.com/tamcore/garmin-mcp/internal/store"
)

// TestTokenSetConversionKeepsTheZeroValueZero keeps an absent record from becoming
// a set of empty credentials, which would then be presented to Garmin as if it
// were one.
func TestTokenSetConversionKeepsTheZeroValueZero(t *testing.T) {
	t.Parallel()

	if !toAuth(store.TokenSet{}).IsZero() {
		t.Error("an absent stored set became a non-zero credential set")
	}
	if !StoreTokenSet(auth.TokenSet{}).IsZero() {
		t.Error("an absent credential set became a non-zero stored set")
	}

	expiry := time.Unix(0, 0).UTC()
	stored := store.NewTokenSet(
		"synthetic-di-token", "synthetic-refresh", "synthetic-client", expiry)

	round := StoreTokenSet(toAuth(stored))
	if round.Token() != stored.Token() || round.RefreshToken() != stored.RefreshToken() {
		t.Error("the round trip did not preserve the token material")
	}
	if !round.ExpiresAt().Equal(expiry) {
		t.Errorf("ExpiresAt = %v, want %v", round.ExpiresAt(), expiry)
	}
}
