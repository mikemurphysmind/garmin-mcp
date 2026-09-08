package store

import (
	"fmt"
	"strings"
)

// Inline token JSON is an explicitly insecure compatibility override.
//
// Upstream accepts a tokenstore argument that is either a path or the token JSON
// itself. Passing the JSON inline means the refresh token travels as a command-line
// argument, an environment variable or a configuration value, where it is visible
// to `ps`, to process inspection, to shell history, to container inspect output and
// to crash reports. Prefer a mounted owner-only file.
//
// Therefore:
//
//   - remote mode never reaches this path: it keeps its token sets in the
//     database, and only the stdio composition root imports a configured
//     document;
//   - no error built here ever contains the value. Only the source kind and the
//     length are reported.
//
// Source: the 0.3.10 login() failure path, which logs source and length precisely
// because the value may be the inline token JSON.

// ParseInlineTokenJSON parses inline 0.3.x token JSON.
//
// It reports ErrIncompatibleTokenFile when the value is not a 0.3.x document. No
// error names the value.
func ParseInlineTokenJSON(value string) (TokenSet, error) {
	raw := []byte(strings.TrimSpace(value))
	if !IsLegacyTokenDocument(raw) {
		return TokenSet{}, fmt.Errorf("store: inline value is not a 0.3.x token document "+
			"(source=%s, %d bytes): %w", sourceInline, len(raw), ErrIncompatibleTokenFile)
	}
	return decodeTokenDocument(raw, sourceInline)
}
