package oauthserver_test

import (
	"errors"
	"testing"

	"github.com/tamcore/garmin-mcp/internal/oauthserver"
)

func TestParseRedirectPatternAcceptsATrailingPathWildcard(t *testing.T) {
	pattern, err := oauthserver.ParseRedirectPattern("https://chatgpt.com/a/b/c/*")
	if err != nil {
		t.Fatalf("ParseRedirectPattern: %v", err)
	}
	if got := pattern.String(); got != "https://chatgpt.com/a/b/c/*" {
		t.Fatalf("String() = %q, want the registered bytes", got)
	}
	if pattern.IsZero() {
		t.Fatal("a parsed pattern reports itself as the zero value")
	}
}

func TestParseRedirectPatternRefusesEveryOtherWildcardShape(t *testing.T) {
	cases := map[string]string{
		"wildcard not final":      "https://chatgpt.com/a/*/c",
		"two wildcards":           "https://chatgpt.com/a/*/c/*",
		"wildcard mid-segment":    "https://chatgpt.com/a/b/pre*",
		"wildcard in the host":    "https://*.chatgpt.com/cb/*",
		"host is only a wildcard": "https://*",
		"scheme wildcard":         "*://chatgpt.com/cb/*",
		"no wildcard at all":      "https://chatgpt.com/a/b/c",
		"prefix carries a query":  "https://chatgpt.com/cb?next=1/*",
		// pathBody finds the query's own trailing "/" as the first slash and
		// returns "", which isSafePathRemainder reports safe; only the
		// explicit "?" check refuses this.
		"query-only prefix":                "https://host?x=1/*",
		"prefix carries a fragment":        "https://chatgpt.com/cb#f/*",
		"prefix carries userinfo":          "https://user@chatgpt.com/cb/*",
		"prefix is plaintext":              "http://chatgpt.com/cb/*",
		"prefix has no host":               "https:///cb/*",
		"upper-case scheme":                "HTTPS://chatgpt.com/cb/*",
		"empty":                            "",
		"bare wildcard":                    "*",
		"prefix path has parent traversal": "https://chatgpt.com/a/../*",
		"prefix path has dot segment":      "https://chatgpt.com/a/./*",
		"prefix path has empty segment":    "https://chatgpt.com/a//*",
		"prefix path has percent-encoding": "https://chatgpt.com/a/%2e%2e/*",
		"prefix path has a backslash":      "https://chatgpt.com/a\\b/*",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := oauthserver.ParseRedirectPattern(raw); !errors.Is(err, oauthserver.ErrInvalidRedirectURI) {
				t.Fatalf("ParseRedirectPattern(%q) error = %v, want ErrInvalidRedirectURI", raw, err)
			}
		})
	}
}

func TestParseRedirectPatternAcceptsALoopbackPrefix(t *testing.T) {
	if _, err := oauthserver.ParseRedirectPattern("http://127.0.0.1/cb/*"); err != nil {
		t.Fatalf("ParseRedirectPattern on a literal loopback prefix: %v", err)
	}
}

func TestIsRedirectPatternReportsTheTrailingWildcard(t *testing.T) {
	if !oauthserver.IsRedirectPattern("https://chatgpt.com/cb/*") {
		t.Fatal("a trailing wildcard is not reported as a pattern")
	}
	if oauthserver.IsRedirectPattern("https://chatgpt.com/cb") {
		t.Fatal("an exact URI is reported as a pattern")
	}
}

func mustPattern(t *testing.T, raw string) oauthserver.RedirectPattern {
	t.Helper()
	pattern, err := oauthserver.ParseRedirectPattern(raw)
	if err != nil {
		t.Fatalf("ParseRedirectPattern(%q): %v", raw, err)
	}
	return pattern
}

func mustRedirect(t *testing.T, raw string) oauthserver.RedirectURI {
	t.Helper()
	uri, err := oauthserver.ParseRedirectURI(raw)
	if err != nil {
		t.Fatalf("ParseRedirectURI(%q): %v", raw, err)
	}
	return uri
}

func TestRedirectPatternMatchesANormalizedRemainder(t *testing.T) {
	pattern := mustPattern(t, "https://chatgpt.com/a/b/c/*")
	for _, raw := range []string{
		"https://chatgpt.com/a/b/c/x",
		"https://chatgpt.com/a/b/c/x/y",
		"https://chatgpt.com/a/b/c/x-y_z.1",
		"https://chatgpt.com/a/b/c/x-y_z.1~2",
	} {
		t.Run(raw, func(t *testing.T) {
			if !pattern.Matches(mustRedirect(t, raw)) {
				t.Fatalf("Matches(%q) = false, want true", raw)
			}
		})
	}
}

func TestRedirectPatternRefusesEveryWideningRemainder(t *testing.T) {
	pattern := mustPattern(t, "https://chatgpt.com/a/b/c/*")
	cases := map[string]string{
		// The prefix itself is not a match: register it separately if wanted.
		"bare prefix": "https://chatgpt.com/a/b/c/",
		// Traversal: these match the prefix bytes but a browser normalizes them
		// to a path outside it. This is the load-bearing check.
		"parent traversal":   "https://chatgpt.com/a/b/c/../../evil",
		"trailing traversal": "https://chatgpt.com/a/b/c/x/..",
		"dot segment":        "https://chatgpt.com/a/b/c/./x",
		"empty segment":      "https://chatgpt.com/a/b/c//x",
		// A query would ride into WithParams and collide with the response.
		"remainder query": "https://chatgpt.com/a/b/c/x?code=stolen",
		// A different origin or path, however similar.
		"different host":        "https://evil.example/a/b/c/x",
		"host case differs":     "https://CHATGPT.com/a/b/c/x",
		"prefix is a substring": "https://chatgpt.com/a/b/cx/y",
		"shorter path":          "https://chatgpt.com/a/b/x",
		"scheme differs":        "http://127.0.0.1/a/b/c/x",
		// Percent-encoded and backslash traversal: the allowlist closes these.
		"percent-encoded dot segment":       "https://chatgpt.com/a/b/c/%2e%2e/evil",
		"upper-case percent-encoded":        "https://chatgpt.com/a/b/c/%2E%2E/evil",
		"mixed literal and encoded, first":  "https://chatgpt.com/a/b/c/.%2e/evil",
		"mixed literal and encoded, second": "https://chatgpt.com/a/b/c/%2e./evil",
		"backslash traversal":               "https://chatgpt.com/a/b/c/..\\..\\evil",
		"backslash as separator":            "https://chatgpt.com/a/b/c/x\\y",
		"percent-encoded slash":             "https://chatgpt.com/a/b/c/x%2fy",
		"bare percent":                      "https://chatgpt.com/a/b/c/50%25",
		"trailing slash":                    "https://chatgpt.com/a/b/c/x/",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			// The input must parse as an exact redirect URI; a case that cannot
			// parse belongs in its own parse-refusal test, not here, so this
			// loop cannot silently stop exercising Matches.
			candidate := mustRedirect(t, raw)
			if pattern.Matches(candidate) {
				t.Fatalf("Matches(%q) = true, want false", raw)
			}
		})
	}
}

// TestRedirectPatternRefusesUserinfoAfterFirstSlash is the sharpest widening
// case: with prefix "https://a/b/" and presented "https://a/b/c@evil.example/x",
// url.Parse resolves Host as exactly "a", so checkOrigin's userinfo check never
// fires (the "@" is not in the authority component). Only the remainder byte
// allowlist in isSafePathRemainder refuses this, because "@" is not in
// A-Za-z0-9-._~/.
func TestRedirectPatternRefusesUserinfoAfterFirstSlash(t *testing.T) {
	pattern := mustPattern(t, "https://a/b/*")
	candidate := mustRedirect(t, "https://a/b/c@evil.example/x")
	if pattern.Matches(candidate) {
		t.Fatal("Matches(...) = true, want false: userinfo-shaped remainder after the first slash must be refused")
	}
}

func TestZeroRedirectPatternMatchesNothing(t *testing.T) {
	var pattern oauthserver.RedirectPattern
	if pattern.Matches(mustRedirect(t, "https://chatgpt.com/a/b/c/x")) {
		t.Fatal("the zero pattern matched a redirect URI")
	}
	if !pattern.IsZero() || pattern.String() != "" {
		t.Fatal("the zero pattern does not report itself as zero")
	}
}
