//go:build fakegarmin

package auth_test

import (
	"net/http"
	"testing"

	"github.com/tamcore/garmin-mcp/internal/garmin/protocol"
	"github.com/tamcore/garmin-mcp/internal/testkit"
)

// A path-scoped SSO session cookie must survive the handoff to MFA.
// All requests use the fake Garmin server; no real account is involved.
func TestMFAKeepsPathScopedSessionCookie(t *testing.T) {
	for _, cookiePath := range []string{"/", "/mobile", "/mobile/api"} {
		t.Run(cookiePath, func(t *testing.T) {
			login := testkit.JSON(http.StatusOK, testkit.LoginMFARequiredJSON(protocol.MFAMethodEmail))
			login.Header = http.Header{"Set-Cookie": {"MFA_SESSION=synthetic-session; Path=" + cookiePath + "; HttpOnly"}}
			h := newHarness(t, mobileMFAScript().With(protocol.PathMobileLogin, login))
			capability := startMFA(t, h)
			if _, err := h.auth.CompleteMFA(t.Context(), capability, testPrincipal, testMFACode); err != nil {
				t.Fatalf("CompleteMFA: %v", err)
			}
			for _, req := range h.server.Requests() {
				if req.Path != protocol.PathMobileMFAVerifyCode {
					continue
				}
				httpReq := &http.Request{Header: req.Header}
				cookie, err := httpReq.Cookie("MFA_SESSION")
				if err != nil || cookie.Value != "synthetic-session" {
					t.Fatalf("MFA verification lost the session cookie scoped to %s", cookiePath)
				}
				return
			}
			t.Fatal("MFA verification request was not sent")
		})
	}
}

// Match the portal fallback used by the successful manual diagnostic: mobile
// login is rate-limited, the widget cannot continue, and portal login needs MFA.
func TestPortalMFAKeepsPathScopedSessionAfterFallback(t *testing.T) {
	login := testkit.JSON(http.StatusOK, testkit.LoginMFARequiredJSON(protocol.MFAMethodEmail))
	login.Header = http.Header{"Set-Cookie": {"PORTAL_SESSION=synthetic-portal; Path=/portal; HttpOnly"}}
	script := baseScript().
		With(protocol.PathMobileLogin, testkit.RateLimited(30)).
		With(protocol.PathWidgetEmbed, testkit.HTML(http.StatusFound, "")).
		With(protocol.PathPortalSignInPage, testkit.HTML(http.StatusOK, "sign in")).
		With(protocol.PathPortalLogin, login).
		With(protocol.PathPortalMFAVerifyCode, testkit.JSON(http.StatusOK, testkit.LoginSuccessJSON(testTicket)))
	h := newHarness(t, script)
	capability := startMFA(t, h)
	if _, err := h.auth.CompleteMFA(t.Context(), capability, testPrincipal, testMFACode); err != nil {
		t.Fatalf("CompleteMFA: %v", err)
	}
	for _, req := range h.server.Requests() {
		if req.Path != protocol.PathPortalMFAVerifyCode {
			continue
		}
		httpReq := &http.Request{Header: req.Header}
		cookie, err := httpReq.Cookie("PORTAL_SESSION")
		if err != nil || cookie.Value != "synthetic-portal" {
			t.Fatal("portal MFA verification lost the session cookie")
		}
		return
	}
	t.Fatal("portal MFA verification request was not sent")
}
