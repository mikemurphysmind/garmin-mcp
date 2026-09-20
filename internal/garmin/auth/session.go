package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// Doer performs one HTTP request. It is the pluggable transport of the login
// state machine: production injects an *http.Client, and a test injects the
// guarded Doer from internal/testkit, which cannot leave the fake server's
// origin.
//
// The interface lives with its consumer and has exactly one method, so no
// implementation detail of the caller's transport leaks into this package.
type Doer interface {
	// Do performs req and returns its response.
	Do(req *http.Request) (*http.Response, error)
}

// maxResponseBytes bounds a login response body. Garmin's login pages and JSON
// bodies are far smaller; anything larger is refused rather than truncated,
// because a truncated body classifies unreliably.
const maxResponseBytes = 1 << 20

// Errors raised while performing a request. Neither renders a URL or a body.
var (
	// errResponseTooLarge reports a login response over maxResponseBytes.
	errResponseTooLarge = errors.New("garmin auth: login response exceeds the size bound")
	// errMalformedURL replaces a URL-bearing error message.
	errMalformedURL = errors.New("malformed request URL")
)

// rawResponse is one HTTP response, already read into memory. It is the internal
// currency of this package: the classified flows turn it into a
// protocol.Response, and the DI token endpoint decodes its body directly.
type rawResponse struct {
	status int
	header http.Header
	body   []byte
}

// session is one SSO conversation: a transport plus the cookie jar the flow
// accumulates. It is created per login attempt and per MFA continuation, so two
// interleaved logins never share cookies.
type session struct {
	doer Doer
	jar  *cookiejar.Jar
	// Keep Set-Cookie attributes for an MFA handoff. Jar.Cookies returns only
	// cookies matching one request path and drops their original attributes.
	cookieHistory map[string][]*http.Cookie
}

// newSession returns an empty session over doer.
func newSession(doer Doer) (*session, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("garmin auth: create cookie jar: %w", err)
	}
	return &session{doer: doer, jar: jar, cookieHistory: make(map[string][]*http.Cookie)}, nil
}

// seed installs cookies for rawURL, so an MFA continuation resumes the SSO
// session the credential POST established.
func (s *session) seed(rawURL string, cookies []*http.Cookie) error {
	if len(cookies) == 0 {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("garmin auth: seed session cookies: %w", errMalformedURL)
	}
	s.jar.SetCookies(parsed, cookies)
	return nil
}

// cookieSnapshot returns the Set-Cookie history for one origin, including
// path-scoped cookies and deletions. Replay through a fresh jar preserves its
// normal domain, path, expiry and Secure checks. Every value is a credential.
func (s *session) cookieSnapshot(rawURL string) []*http.Cookie {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	return copyCookies(s.cookieHistory[parsed.Scheme+"://"+parsed.Host])
}

// get performs a GET.
func (s *session) get(ctx context.Context, rawURL string, header http.Header) (rawResponse, error) {
	return s.do(ctx, http.MethodGet, rawURL, header, nil, "")
}

// postJSON performs a POST with a JSON body. The payload is marshaled and
// discarded inside this call, so credential material never outlives it.
func (s *session) postJSON(
	ctx context.Context,
	rawURL string,
	header http.Header,
	payload any,
) (rawResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return rawResponse{}, fmt.Errorf("garmin auth: encode request body: %w", err)
	}
	return s.do(ctx, http.MethodPost, rawURL, header, body, "application/json")
}

// postForm performs a POST with a url-encoded body.
func (s *session) postForm(
	ctx context.Context,
	rawURL string,
	header http.Header,
	form url.Values,
) (rawResponse, error) {
	return s.do(ctx, http.MethodPost, rawURL, header, []byte(form.Encode()),
		"application/x-www-form-urlencoded")
}

// do performs one request, applying and collecting cookies, and reads a bounded
// body into a rawResponse.
func (s *session) do(
	ctx context.Context,
	method, rawURL string,
	header http.Header,
	body []byte,
	contentType string,
) (rawResponse, error) {
	req, err := s.newRequest(ctx, method, rawURL, header, body, contentType)
	if err != nil {
		return rawResponse{}, err
	}

	resp, err := s.doer.Do(req)
	if err != nil {
		return rawResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	read, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return rawResponse{}, fmt.Errorf("garmin auth: read response: %w", err)
	}
	if len(read) > maxResponseBytes {
		return rawResponse{}, errResponseTooLarge
	}

	if cookies := resp.Cookies(); len(cookies) > 0 {
		s.jar.SetCookies(req.URL, cookies)
		origin := req.URL.Scheme + "://" + req.URL.Host
		for _, cookie := range cookies {
			s.cookieHistory[origin] = append(s.cookieHistory[origin], snapshotCookie(req.URL, cookie, time.Now()))
		}
	}
	return rawResponse{status: resp.StatusCode, header: resp.Header.Clone(), body: read}, nil
}

// snapshotCookie resolves attributes relative to the original response, so
// replay at the origin does not widen a default Path or restart Max-Age.
func snapshotCookie(u *url.URL, cookie *http.Cookie, now time.Time) *http.Cookie {
	cloned := *cookie
	if cloned.Path == "" || !strings.HasPrefix(cloned.Path, "/") {
		cloned.Path = "/"
		if last := strings.LastIndex(u.Path, "/"); strings.HasPrefix(u.Path, "/") && last > 0 {
			cloned.Path = u.Path[:last]
		}
	}
	if cloned.MaxAge > 0 {
		cloned.Expires = now.Add(time.Duration(cloned.MaxAge) * time.Second)
		cloned.MaxAge = 0
	}
	// These copies of the wire header are not used by SetCookies. Dropping them
	// avoids retaining uncounted, redundant secret material in Pending.
	cloned.Raw, cloned.RawExpires, cloned.Unparsed = "", "", nil
	return &cloned
}

// newRequest builds the request, copies header, attaches the jar's cookies and
// sets the content type.
func (s *session) newRequest(
	ctx context.Context,
	method, rawURL string,
	header http.Header,
	body []byte,
	contentType string,
) (*http.Request, error) {
	var reader io.Reader = http.NoBody
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		// The wrapped message would echo the URL, which carries a query string.
		return nil, fmt.Errorf("garmin auth: build %s request: %w", method, errMalformedURL)
	}

	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, cookie := range s.jar.Cookies(req.URL) {
		req.AddCookie(cookie)
	}
	return req, nil
}

// withQuery appends query to rawURL. Credentials never go here: only client ids,
// locales and service URLs.
func withQuery(rawURL string, query url.Values) string {
	if len(query) == 0 {
		return rawURL
	}
	separator := "?"
	if strings.Contains(rawURL, "?") {
		separator = "&"
	}
	return rawURL + separator + query.Encode()
}
