package auth

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestSnapshotCookiePreservesScopeAndExpiry(t *testing.T) {
	u, err := url.Parse("https://sso.example.invalid/mobile/api/login")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	original := &http.Cookie{Name: "session", Value: "synthetic", Secure: true, MaxAge: 20}
	snapshot := snapshotCookie(u, original, now)
	if snapshot.Path != "/mobile/api" || !snapshot.Secure || snapshot.MaxAge != 0 ||
		!snapshot.Expires.Equal(now.Add(20*time.Second)) {
		t.Fatal("snapshot changed the cookie scope or restarted its lifetime")
	}
	if original.Path != "" || original.MaxAge != 20 || !original.Expires.IsZero() {
		t.Fatal("snapshot mutated its input")
	}
}

func TestCookieSnapshotReplayHonorsScopeExpiryAndDeletion(t *testing.T) {
	base := "https://sso.example.invalid"
	u, err := url.Parse(base + "/mobile/api/login")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := newSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cookies := []*http.Cookie{
		{Name: "root", Value: "synthetic-root", Path: "/"},
		{Name: "path", Value: "synthetic-path", Secure: true},
		{Name: "expired", Value: "synthetic-expired", Path: "/", Expires: now.Add(-time.Hour)},
		{Name: "deleted", Value: "synthetic-deleted", Path: "/"},
		{Name: "deleted", Path: "/", MaxAge: -1},
	}
	for _, cookie := range cookies {
		sess.cookieHistory[base] = append(sess.cookieHistory[base], snapshotCookie(u, cookie, now))
	}
	restored, err := newSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.seed(base, sess.cookieSnapshot(base)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		url  string
		want int
	}{
		{base + "/mobile/api/mfa/verifyCode", 2},
		{base + "/portal/api/mfa/verifyCode", 1},
		{"http://sso.example.invalid/mobile/api/mfa/verifyCode", 1},
		{"https://other.example.invalid/mobile/api/mfa/verifyCode", 0},
	} {
		target, parseErr := url.Parse(tc.url)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if got := len(restored.jar.Cookies(target)); got != tc.want {
			t.Errorf("cookie count for %s = %d, want %d", tc.url, got, tc.want)
		}
	}
	copy := sess.cookieSnapshot(base)
	copy[0].Value = "mutated"
	if sess.cookieSnapshot(base)[0].Value != "synthetic-root" {
		t.Fatal("snapshot aliases stored cookie values")
	}
	if len(sess.cookieSnapshot("https://other.example.invalid")) != 0 {
		t.Fatal("snapshot crossed origins")
	}
}
