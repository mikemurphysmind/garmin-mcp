package cmd

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/tamcore/garmin-mcp/internal/config"
)

const (
	wildcardClientID    = "chatgpt"
	wildcardClientName  = "ChatGPT"
	wildcardResource    = "https://mcp.example"
	wildcardRedirectURI = "https://chatgpt.com/cb/*"
)

// wildcardClient returns a client registration whose sole redirect URI is a
// trailing-path wildcard pattern.
func wildcardClient() config.OAuthClient {
	return config.OAuthClient{
		ID:           wildcardClientID,
		Name:         wildcardClientName,
		RedirectURIs: []string{wildcardRedirectURI},
		Scopes:       []string{remoteScope},
		Resources:    []string{wildcardResource},
		Public:       true,
	}
}

// TestConfigClientsRefuseAPatternWithoutTheAcknowledgement keeps a wildcard
// redirect registration unregistrable until the operator sets the acknowledgement,
// regardless of whether a logger is supplied.
func TestConfigClientsRefuseAPatternWithoutTheAcknowledgement(t *testing.T) {
	cfg := config.Config{OAuthClients: []config.OAuthClient{wildcardClient()}}
	if _, err := newConfigClients(cfg, nil); err == nil {
		t.Fatal("a pattern was registered without the acknowledgement")
	}
}

// TestConfigClientsAcceptAPatternWithTheAcknowledgementAndWarn proves the
// acknowledgement reaches the registered client and that start-up logs a warning
// naming both the client and the pattern it registered, so the weaker match stays
// visible in every log even though the operator set the flag once, long before.
func TestConfigClientsAcceptAPatternWithTheAcknowledgementAndWarn(t *testing.T) {
	var sink bytes.Buffer
	events := slog.New(slog.NewTextHandler(&sink, nil))
	cfg := config.Config{
		OAuthAllowRedirectWildcards: true,
		OAuthClients:                []config.OAuthClient{wildcardClient()},
	}
	registry, err := newConfigClients(cfg, events)
	if err != nil {
		t.Fatalf("newConfigClients: %v", err)
	}
	if _, err := registry.Client(t.Context(), wildcardClientID); err != nil {
		t.Fatalf("the registered client is not resolvable: %v", err)
	}
	log := sink.String()
	if !strings.Contains(log, "WARN") {
		t.Fatalf("start-up logged no warning for a wildcard registration: %s", log)
	}
	if !strings.Contains(log, "client_id="+wildcardClientID) {
		t.Fatalf("the warning does not name the client: %s", log)
	}
	if !strings.Contains(log, "chatgpt.com/cb/") {
		t.Fatalf("the warning does not name the registered pattern: %s", log)
	}
}

// TestConfigClientsWarnOncePerPattern proves the warning is per registered
// pattern, not per client: a client with two wildcard redirects produces two
// warning records.
func TestConfigClientsWarnOncePerPattern(t *testing.T) {
	var sink bytes.Buffer
	events := slog.New(slog.NewTextHandler(&sink, nil))
	client := wildcardClient()
	client.RedirectURIs = []string{
		"https://chatgpt.com/a/*",
		"https://chatgpt.com/b/*",
	}
	cfg := config.Config{
		OAuthAllowRedirectWildcards: true,
		OAuthClients:                []config.OAuthClient{client},
	}
	if _, err := newConfigClients(cfg, events); err != nil {
		t.Fatalf("newConfigClients: %v", err)
	}
	if got := strings.Count(sink.String(), "WARN"); got != 2 {
		t.Fatalf("logged %d warnings, want one per registered pattern: %s", got, sink.String())
	}
}

// TestConfigClientsNilLoggerIsAcceptedWithTheAcknowledgement proves a nil logger
// is a legitimate caller shape once the pattern itself is admitted: the
// composition root must not panic when no sink was built yet.
func TestConfigClientsNilLoggerIsAcceptedWithTheAcknowledgement(t *testing.T) {
	cfg := config.Config{
		OAuthAllowRedirectWildcards: true,
		OAuthClients:                []config.OAuthClient{wildcardClient()},
	}
	if _, err := newConfigClients(cfg, nil); err != nil {
		t.Fatalf("newConfigClients with a nil logger: %v", err)
	}
}
