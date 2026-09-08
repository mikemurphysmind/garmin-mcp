//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// TestTokenEndpointRequiresTheMatchingPKCEVerifier is the mutant this test
// catches: a build that skipped, inverted or no-op'd CodeChallenge.Verify would
// accept a wrong or absent verifier and mint a token for whoever holds the
// leaked code alone, which defeats the entire point of PKCE.
//
// codeGrant consumes a code atomically before any binding is checked, so a
// build that returned 400 invalid_grant unconditionally — never actually
// verifying PKCE — would pass the negative cases below for the wrong reason.
// The positive control at the end rules that out: the identical fixture, with
// the correct verifier and a fresh code, must succeed.
//
// All three codes are seeded before the server process starts; see
// setUpOAuthFlow.
func TestTokenEndpointRequiresTheMatchingPKCEVerifier(t *testing.T) {
	fixture := setUpOAuthFlow(t, 3)
	verifier := fixture.verifier

	wrongVerifierCode := fixture.nextCode(t)
	noVerifierCode := fixture.nextCode(t)
	controlCode := fixture.nextCode(t)

	cases := map[string]struct {
		code     string
		verifier string
	}{
		"a wrong verifier": {wrongVerifierCode, "wrong-verifier-0123456789wrong-verifier-012"},
		"no verifier":      {noVerifierCode, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			form := tokenForm(tc.code, remoteRedirectURI, tc.verifier)

			response, body := postToken(t, fixture.server, form)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for %s (error %s)",
					response.StatusCode, name, safeTokenFailure(body))
			}
			if failure := decodeTokenError(t, body); failure.Error != "invalid_grant" {
				t.Errorf("error = %q, want invalid_grant for %s", failure.Error, name)
			}
		})
	}

	t.Run("positive control: the correct verifier succeeds", func(t *testing.T) {
		form := tokenForm(controlCode, remoteRedirectURI, verifier)

		response, body := postToken(t, fixture.server, form)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 for the correct verifier (error %s)",
				response.StatusCode, safeTokenFailure(body))
		}
	})
}

// TestTokenEndpointRequiresTheExactRedirectURI is the mutant this test catches:
// a build that compared the presented redirect URI loosely (by prefix, by host
// only, or not at all) would let a code stolen from one client's callback be
// redeemed against an attacker's own redirect target.
//
// The positive control at the end is the same reasoning as the PKCE test
// above: an unconditional 400 invalid_grant would otherwise pass the negative
// assertion for the wrong reason, since a not-found or already-consumed code
// yields the identical response.
// Both codes are seeded before the server process starts; see setUpOAuthFlow.
func TestTokenEndpointRequiresTheExactRedirectURI(t *testing.T) {
	fixture := setUpOAuthFlow(t, 2)
	verifier := fixture.verifier

	mismatched := fixture.nextCode(t)
	matching := fixture.nextCode(t)

	form := tokenForm(mismatched, remoteRedirectURI+"-attacker", verifier)
	response, body := postToken(t, fixture.server, form)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a mismatched redirect URI (error %s)",
			response.StatusCode, safeTokenFailure(body))
	}
	if failure := decodeTokenError(t, body); failure.Error != "invalid_grant" {
		t.Errorf("error = %q, want invalid_grant for a mismatched redirect URI", failure.Error)
	}

	controlForm := tokenForm(matching, remoteRedirectURI, verifier)
	controlResponse, controlBody := postToken(t, fixture.server, controlForm)
	if controlResponse.StatusCode != http.StatusOK {
		t.Fatalf("positive control: status = %d, want 200 for the exact redirect URI (error %s)",
			controlResponse.StatusCode, safeTokenFailure(controlBody))
	}
}

// concurrentTokenAttempt is the outcome of one goroutine's redemption attempt.
type concurrentTokenAttempt struct {
	status       int
	body         []byte
	cacheControl string
}

// redeemConcurrently fires n simultaneous redemptions of the same form against
// server and returns each one's outcome, in no particular order. It never
// calls into *testing.T from a goroutine other than the caller's, because
// t.Fatalf is not safe to call from any other one.
func redeemConcurrently(t *testing.T, server remoteServer, form url.Values, n int) []concurrentTokenAttempt {
	t.Helper()

	attempts := make([]concurrentTokenAttempt, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Go(func() {
			response, err := server.client.PostForm(server.origin+"/token", form)
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = response.Body.Close() }()
			body, readErr := io.ReadAll(response.Body)
			if readErr != nil {
				errs[i] = readErr
				return
			}
			attempts[i] = concurrentTokenAttempt{
				status:       response.StatusCode,
				body:         body,
				cacheControl: response.Header.Get("Cache-Control"),
			}
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent redemption %d: %v", i, err)
		}
	}
	return attempts
}

// TestTokenEndpointConsumesOneCodeAtomicallyUnderConcurrency is the mutant this
// test catches: a build whose code consumption is not atomic, or that marks a
// code used only after minting tokens rather than before, would let a captured
// code be redeemed twice and mint two independent token pairs from one
// authorization.
//
// A serial replay cannot distinguish atomic consumption from non-atomic
// consumption, nor "mark used before minting" from "mark used after minting":
// both shapes pass a request that waits for the first response before sending
// the second. Firing every redemption of one code at once is the property that
// actually needs proving, so this test does that.
//
// The surviving token is also asserted to work. An earlier version of this
// test deliberately did not assert that, because two concurrent redemptions of
// one code reliably produced a 200 whose access token the MCP endpoint then
// refused with invalid_token. That was traced to this package's own test
// harness rather than the product: the harness opened the SQLite database from
// the test process while the server subprocess held it open too, and a
// concurrent /token redemption under that two-writer arrangement produced a
// genuine disk I/O error (SQLITE_IOERR_SHORT_READ) on the server's own read
// path. With every code seeded before the server process starts and no write
// to the database after that (see setUpOAuthFlow and the note atop
// seed_test.go), the winning token authenticates reliably, and this assertion
// was restored.
func TestTokenEndpointConsumesOneCodeAtomicallyUnderConcurrency(t *testing.T) {
	const concurrentRedemptions = 8

	fixture := setUpOAuthFlow(t, 1)
	verifier := fixture.verifier
	code := fixture.nextCode(t)
	form := tokenForm(code, remoteRedirectURI, verifier)

	attempts := redeemConcurrently(t, fixture.server, form, concurrentRedemptions)

	var successes int
	var success tokenSuccessResponse
	for i, attempt := range attempts {
		switch attempt.status {
		case http.StatusOK:
			successes++
			if err := json.Unmarshal(attempt.body, &success); err != nil {
				t.Fatalf("decode redemption %d's token response: %v", i, err)
			}
			if attempt.cacheControl != "no-store" {
				t.Errorf("redemption %d Cache-Control = %q, want no-store on a token response",
					i, attempt.cacheControl)
			}
		case http.StatusBadRequest:
			if failure := decodeTokenError(t, attempt.body); failure.Error != "invalid_grant" {
				t.Errorf("redemption %d error = %q, want invalid_grant", i, failure.Error)
			}
		default:
			t.Errorf("redemption %d status = %d, want 200 or 400", i, attempt.status)
		}
	}
	if successes != 1 {
		t.Fatalf("%d of %d concurrent redemptions of one code succeeded, want exactly 1: "+
			"code consumption is not atomic", successes, concurrentRedemptions)
	}
	if success.AccessToken == "" {
		t.Fatal("access_token is empty")
	}
	if success.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", success.TokenType)
	}

	mcpResponse := postStatefulInitialize(t, fixture.server, success.AccessToken)
	defer func() { _ = mcpResponse.Body.Close() }()
	if mcpResponse.StatusCode != http.StatusOK {
		t.Errorf("MCP initialize with the surviving concurrent redemption's token: status = %d, want 200",
			mcpResponse.StatusCode)
	}
}

// TestTokenEndpointIssuedTokenAuthenticatesAnMCPRequest is the mutant this test
// catches: a build that broke the wiring between the authorization server and
// the MCP bearer resolver, so a token this endpoint issues is refused by the
// endpoint it was issued for. One redemption, no concurrency — see the note on
// TestTokenEndpointConsumesOneCodeAtomicallyUnderConcurrency for why the two
// properties are tested separately.
func TestTokenEndpointIssuedTokenAuthenticatesAnMCPRequest(t *testing.T) {
	fixture := setUpOAuthFlow(t, 1)
	verifier := fixture.verifier
	code := fixture.nextCode(t)

	success := redeemForToken(t, fixture.server, tokenForm(code, remoteRedirectURI, verifier))

	mcpResponse := postStatefulInitialize(t, fixture.server, success.AccessToken)
	defer func() { _ = mcpResponse.Body.Close() }()
	if mcpResponse.StatusCode != http.StatusOK {
		t.Errorf("MCP initialize with the issued token: status = %d, want 200",
			mcpResponse.StatusCode)
	}
}

// TestAuthorizeEndpointRefusesAPlainPKCEMethod is the mutant this test catches:
// a build that accepted "plain" alongside "S256", or that stopped validating
// the method at all, would let an attacker who can read the authorization
// request (a referrer leak, a shared proxy log) replay the code without ever
// needing the verifier, which is exactly what PKCE exists to prevent. It runs
// at /authorize, before any login, so it needs no seeded state.
func TestAuthorizeEndpointRefusesAPlainPKCEMethod(t *testing.T) {
	server := startRemoteServer(t)
	_, challenge := pkcePair(t)

	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", remoteClientID)
	query.Set("redirect_uri", remoteRedirectURI)
	query.Set("scope", remoteScope)
	query.Set("state", "e2e-plain-pkce-state")
	query.Set("resource", server.mcpURL)
	query.Set("code_challenge_method", "plain")
	query.Set("code_challenge", challenge)

	noRedirect := &http.Client{
		Transport:     server.client.Transport,
		Timeout:       server.client.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := noRedirect.Get(server.origin + "/authorize?" + query.Encode())
	if err != nil {
		t.Fatalf("get /authorize: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302 redirecting back to the client", response.StatusCode)
	}
	location := response.Header.Get("Location")
	if !strings.HasPrefix(location, remoteRedirectURI) {
		t.Fatalf("Location = %q, want it to start with the registered redirect URI", location)
	}
	redirected, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse the redirect location %q: %v", location, err)
	}
	if got := redirected.Query().Get("error"); got != "invalid_request" {
		t.Errorf("error = %q, want invalid_request for a plain PKCE method", got)
	}
	if got := redirected.Query().Get("state"); got != "e2e-plain-pkce-state" {
		t.Errorf("state = %q, want the client's own state echoed back", got)
	}
}

// TestAPatternFailsStartUpWithoutTheAcknowledgement is the mutant this test
// catches: a build that admitted a wildcard redirect registration regardless
// of oauth-allow-redirect-wildcards — or that checked the setting somewhere
// other than client construction, where a caller could bypass it — would
// start up successfully with a pattern registered and no operator
// acknowledgement, silently carrying the weaker matching rule the setting
// exists to gate.
func TestAPatternFailsStartUpWithoutTheAcknowledgement(t *testing.T) {
	err := startRemoteServerExpectingFailure(t, remoteConfigOptions{
		redirectURIs: []string{"https://client.example/cb/*"},
	})
	if err == nil {
		t.Fatal("a pattern started up without the acknowledgement")
	}
}

// TestAPatternStartsUpWithTheAcknowledgement is the positive control for
// [TestAPatternFailsStartUpWithoutTheAcknowledgement]: the same pattern, with
// oauth-allow-redirect-wildcards set, must start up successfully. Without this
// test the negative case alone cannot distinguish "the acknowledgement gate
// works" from "a registered pattern can never start up at all" — the defect
// Task 12 found in internal/config's own, independent wildcard refusal, which
// rejected a pattern whether or not the acknowledgement was set.
func TestAPatternStartsUpWithTheAcknowledgement(t *testing.T) {
	startRemoteServerCustomized(t, "", remoteConfigOptions{
		redirectURIs: []string{"https://client.example/cb/*"},
		extraLines:   []string{"oauth-allow-redirect-wildcards: true"},
	}, nil)
}

// authorizeStatus drives a GET against /authorize for remoteClientID and
// returns the response status and, when the response is a redirect, its
// Location header. It never follows a redirect: both outcomes this file's
// pattern tests care about — accepted (a 303 to the fixed login route) and
// refused (a 400, rendered locally rather than redirected anywhere) — are
// visible on the first response alone.
func authorizeStatus(t *testing.T, server remoteServer, redirectURI, challenge string) (status int, location string) {
	t.Helper()

	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", remoteClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", remoteScope)
	query.Set("state", "e2e-pattern-state")
	query.Set("resource", server.mcpURL)
	query.Set("code_challenge_method", "S256")
	query.Set("code_challenge", challenge)

	noRedirect := &http.Client{
		Transport:     server.client.Transport,
		Timeout:       server.client.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := noRedirect.Get(server.origin + "/authorize?" + query.Encode())
	if err != nil {
		t.Fatalf("get /authorize: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode, response.Header.Get("Location")
}

// TestAuthorizationSucceedsThroughARedirectPattern proves the
// oauth-allow-redirect-wildcards feature end to end over the real binary,
// now that internal/config's own coarse wildcard gate (fixed in
// b092961) actually admits a pattern behind the acknowledgement: a client
// registered with a trailing-path pattern ("https://client.example/cb/*")
// admits a concrete redirect the configuration never names, that acceptance
// happens at /authorize itself (not merely somewhere downstream), the code
// and token that follow are bound to the exact concrete URI rather than to
// the pattern or to any other URI the same pattern would also admit, and a
// traversal remainder that matches the pattern's prefix bytes is refused
// locally rather than redirected anywhere.
//
// The client row is seeded once, via seedClient, purely to satisfy the
// auth_codes foreign key before the server process starts (see the note atop
// seed_test.go); its redirect URI list is irrelevant, because the server's
// own start-up reconciliation (internal/store, ReconcileClient) overwrites it
// from this deployment's own configuration the moment the process launches —
// which is what actually registers the pattern this test exercises.
func TestAuthorizationSucceedsThroughARedirectPattern(t *testing.T) {
	const concreteRedirect = "https://client.example/cb/session-42"
	const otherRedirect = "https://client.example/cb/other-session"
	const traversalRedirect = "https://client.example/cb/../../evil"

	verifier, challenge := pkcePair(t)
	var code, otherCode string
	server := startRemoteServerCustomized(t, "", remoteConfigOptions{
		redirectURIs: []string{"https://client.example/cb/*"},
		extraLines:   []string{"oauth-allow-redirect-wildcards: true"},
	}, func(dir, origin string) {
		sqlite := openSeedStore(t, dir)
		defer func() { _ = sqlite.Close() }()

		seedClient(t, sqlite)
		principalID := seedPrincipal(t, sqlite, "e2e-pattern@example.test")

		params := seedAuthCodeParams{
			principalID: principalID,
			clientID:    remoteClientID,
			redirectURI: concreteRedirect,
			resource:    mcpURLFor(origin),
			scopes:      []string{remoteScope},
			challenge:   challenge,
		}
		seedConsent(t, sqlite, params)
		code = seedAuthCode(t, sqlite, params)

		// A second code, consented and bound to a different concrete URI that
		// the identical pattern would also admit. It exists to prove the
		// token endpoint binds to the one exact URI a code was issued for,
		// not merely to "some URI the pattern matches" (see the
		// "token binds to the concrete redirect" subtest below).
		otherParams := params
		otherParams.redirectURI = otherRedirect
		seedConsent(t, sqlite, otherParams)
		otherCode = seedAuthCode(t, sqlite, otherParams)
	})

	t.Run("authorize accepts a concrete redirect under the pattern", func(t *testing.T) {
		// Acceptance means resolveClientAndRedirect's MatchRedirectURI found
		// the wildcard match and opened a transaction (303 to the fixed login
		// route), rather than refusing locally with a 400. A build that
		// disabled wildcard matching, or that ignored
		// oauth-allow-redirect-wildcards once past start-up, fails this.
		status, location := authorizeStatus(t, server, concreteRedirect, challenge)
		if status != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303 for a concrete redirect under a registered pattern",
				status)
		}
		if location != "/login" {
			t.Errorf("Location = %q, want the fixed login route", location)
		}
	})

	t.Run("token binds to the concrete redirect, not the pattern", func(t *testing.T) {
		// otherCode was issued for otherRedirect. Presenting it with
		// concreteRedirect instead exercises exactly the same byte-exact
		// comparison codegrant.go always applies (proven independently by
		// TestTokenEndpointRequiresTheExactRedirectURI); here the point is
		// that the comparison happens even though both URIs are admitted by
		// the very same pattern, so a build that bound a code to the pattern
		// string, or to the client generally, rather than the concrete
		// presented URI, would let this redemption succeed and this test
		// catches that.
		mismatchForm := tokenForm(otherCode, concreteRedirect, verifier)
		response, body := postToken(t, server, mismatchForm)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for a code redeemed against a different concrete URI "+
				"under the same pattern (error %s)", response.StatusCode, safeTokenFailure(body))
		}
		if failure := decodeTokenError(t, body); failure.Error != "invalid_grant" {
			t.Errorf("error = %q, want invalid_grant", failure.Error)
		}

		// The positive control: the code redeemed against the exact URI it
		// was issued for succeeds and yields a usable token.
		success := redeemForToken(t, server, tokenForm(code, concreteRedirect, verifier))
		if success.AccessToken == "" {
			t.Fatal("a concrete redirect under a registered pattern did not yield a token")
		}
	})

	t.Run("a traversal redirect is refused locally, not redirected", func(t *testing.T) {
		// The traversal remainder matches the pattern's prefix bytes, but
		// isSafePathRemainder (internal/oauthserver/redirectpattern.go)
		// refuses it before any redirect target can be built, so this must
		// be a local 400 with no Location header — never a redirect toward
		// the traversal target, and never a 303 into the login flow either.
		status, location := authorizeStatus(t, server, traversalRedirect, challenge)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for a traversal redirect that matches the prefix bytes",
				status)
		}
		if location != "" {
			t.Errorf("Location = %q, want no redirect for a refused traversal", location)
		}
	})
}
