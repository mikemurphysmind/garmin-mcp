package oauthserver

import (
	"fmt"
	"net/url"
	"strings"
)

// wildcardSuffix is the only wildcard this server understands: one "*" as the
// final byte of a registered redirect URI, beginning a path segment.
const wildcardSuffix = "*"

// A RedirectPattern is a registered redirect URI whose trailing path is open.
//
// It is deliberately weaker than a [RedirectURI], and it exists only because a
// hosted MCP client can carry a per-installation path an operator cannot know in
// advance. It is refused unless the operator sets the acknowledgement setting,
// and the residual risk is recorded in docs/threat-model.md: an open redirector
// or attacker-influenced content under the wildcarded prefix yields an
// authorization code, and PKCE does not mitigate that.
//
// The pattern keeps the literal prefix — everything before the "*" — and matches
// nothing but a presented URI that begins with those exact bytes and adds a
// normalized, query-free, non-empty remainder.
//
// The zero RedirectPattern is invalid and matches nothing.
type RedirectPattern struct {
	prefix string
}

// IsRedirectPattern reports whether raw is written as a pattern rather than an
// exact redirect URI. It only looks at the shape, so a value it reports true for
// can still fail [ParseRedirectPattern].
func IsRedirectPattern(raw string) bool {
	return strings.HasSuffix(raw, wildcardSuffix)
}

// ParseRedirectPattern validates a trailing-path wildcard registration.
//
// The rules, each closing one way a prefix match can be widened:
//
//   - the "*" is the final byte and appears exactly once, so no wildcard can sit
//     in the scheme, the host, or the middle of the path;
//   - the byte before it is "/", so the wildcard begins a whole path segment and
//     "https://host/cb*" cannot also match "https://host/cb.evil.example";
//   - the prefix passes every [ParseRedirectURI] structural rule, so the scheme,
//     host, userinfo and fragment guarantees are identical to an exact
//     registration;
//   - the prefix's own path is normalized and carries only [isSafePathRemainder]'s
//     safe byte set, so a registration such as "https://host/a/../*" cannot
//     silently widen to the whole host once a browser resolves it;
//   - the prefix carries no query, which would collide with
//     [RedirectURI.WithParams].
func ParseRedirectPattern(raw string) (RedirectPattern, error) {
	prefix, ok := strings.CutSuffix(raw, wildcardSuffix)
	if !ok {
		return RedirectPattern{}, fmt.Errorf(
			"redirect pattern does not end in %q: %w", wildcardSuffix, ErrInvalidRedirectURI)
	}
	if err := checkURIShape(prefix, ErrInvalidRedirectURI); err != nil {
		return RedirectPattern{}, err
	}
	if !strings.HasSuffix(prefix, "/") {
		return RedirectPattern{}, fmt.Errorf(
			"redirect pattern wildcard does not begin a path segment: %w", ErrInvalidRedirectURI)
	}
	parsed, err := url.Parse(prefix)
	if err != nil {
		return RedirectPattern{}, fmt.Errorf(
			"redirect pattern does not parse: %w", ErrInvalidRedirectURI)
	}
	if err := checkOrigin(prefix, parsed, ErrInvalidRedirectURI); err != nil {
		return RedirectPattern{}, err
	}
	if !isSafePathRemainder(pathBody(prefix, parsed)) {
		return RedirectPattern{}, fmt.Errorf(
			"redirect pattern prefix path is not normalized: %w", ErrInvalidRedirectURI)
	}
	// Not redundant with isSafePathRemainder above: for a query-only prefix such
	// as "https://host?x=1/", pathBody finds the query string's own trailing "/"
	// as the first slash and returns "", and isSafePathRemainder("") reports
	// safe. This check is the only thing refusing a prefix carrying a query.
	if strings.Contains(prefix, "?") {
		return RedirectPattern{}, fmt.Errorf(
			"redirect pattern carries a query: %w", ErrInvalidRedirectURI)
	}
	return RedirectPattern{prefix: prefix}, nil
}

// pathBody returns the path of raw with its leading and, since a
// [RedirectPattern] prefix always ends in "/", its one trailing slash removed.
// It reads the bytes directly out of raw rather than through parsed.Path or
// parsed.EscapedPath: those decode or re-escape percent sequences, and this
// package's whole point is to judge a registration on the exact bytes a
// browser would send, not on Go's idea of their meaning.
func pathBody(raw string, parsed *url.URL) string {
	afterScheme := strings.TrimPrefix(raw, parsed.Scheme+"://")
	idx := strings.IndexByte(afterScheme, '/')
	if idx < 0 {
		return ""
	}
	path := afterScheme[idx:]
	return strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/")
}

// String returns the exact bytes the pattern was registered as.
func (p RedirectPattern) String() string {
	if p.prefix == "" {
		return ""
	}
	return p.prefix + wildcardSuffix
}

// IsZero reports whether p is the zero value.
func (p RedirectPattern) IsZero() bool { return p.prefix == "" }

// Equal reports byte-exact equality of two patterns.
func (p RedirectPattern) Equal(other RedirectPattern) bool {
	return p.prefix != "" && p.prefix == other.prefix
}

// Matches reports whether candidate is admitted by this pattern.
//
// candidate has already passed [ParseRedirectURI], so it cannot carry a
// literal "*", a fragment, userinfo, a control byte, or plaintext outside
// loopback. What is left to check is everything that could make a prefix
// comparison mean less than it appears to: the remainder must be non-empty, so
// the bare prefix is not a match, and it must pass [isSafePathRemainder].
func (p RedirectPattern) Matches(candidate RedirectURI) bool {
	if p.prefix == "" || candidate.IsZero() {
		return false
	}
	remainder, ok := strings.CutPrefix(candidate.String(), p.prefix)
	if !ok || remainder == "" {
		return false
	}
	return isSafePathRemainder(remainder)
}

// isSafePathRemainder reports whether s is free of every byte and segment that
// could make a browser resolve the path somewhere a byte-for-byte prefix
// comparison did not see coming.
//
// This is an allowlist, not a blocklist, on purpose. A blocklist that only
// refuses a literal "." or ".." segment misses two real bypasses: a
// percent-encoded dot segment ("%2e%2e", mixed-case included), which the
// WHATWG URL Standard resolves exactly like a literal "..", and a backslash
// used as a path separator, which every WHATWG "special scheme" — https and
// http included — treats exactly like "/". Both defeat a check that only
// compares segments against the literal strings "." and "..". Restricting the
// whole remainder up front to letters, digits, "-", ".", "_", "~" and "/"
// closes every such trick at once: neither "%" nor "\" can appear at all, so
// the only spelling of a dot segment left for the segment check below to catch
// is the literal one.
//
// The empty string is reported safe: it carries no segment and therefore no
// traversal, and every call site that treats an empty remainder as invalid
// (a bare, wildcard-free prefix) checks that before calling this function.
func isSafePathRemainder(s string) bool {
	for i := range len(s) {
		if !isSafePathByte(s[i]) {
			return false
		}
	}
	for segment := range strings.SplitSeq(s, "/") {
		if segment == "." || segment == ".." {
			return false
		}
		if segment == "" && s != "" {
			return false
		}
	}
	return true
}

// isSafePathByte reports whether b may appear in a matched or registered
// remainder path: unreserved RFC 3986 bytes plus the "/" segment separator.
func isSafePathByte(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return true
	}
	switch b {
	case '-', '.', '_', '~', '/':
		return true
	}
	return false
}
