package loginweb

import (
	"fmt"
	"slices"
	"strings"
)

// MaxAllowedEmails bounds the login allowlist. It is a hand-maintained operator
// list, not a user directory, so a few hundred is generous and a larger value
// means the operator meant something this setting does not do.
const MaxAllowedEmails = 256

// An EmailAllowlist restricts which Garmin accounts may complete the remote
// browser login.
//
// The zero value is **open**: it permits every address, which is the behavior a
// deployment had before this setting existed. That is deliberate — an allowlist
// that failed closed when unset would lock out every existing deployment on
// upgrade — and it is why the value is only ever built from validated
// configuration.
//
// It gates login, which is where a principal is created. It is not a kill
// switch: removing an address does not terminate a principal that already
// exists.
//
// The addresses sit behind a pointer, one level deeper than this type, because
// fmt reaches its badVerb path for an unsupported verb and re-prints the value
// at depth 0, where it would dereference and print an unexported field verbatim.
// An account address is the account identifier and is not printable. See
// [allowedAddresses] for why the slice itself needs one further level still.
type EmailAllowlist struct {
	permitted *allowedAddresses
}

// allowedAddresses holds the folded addresses one pointer below EmailAllowlist.
//
// folded is itself a pointer to the slice, not the slice directly: fmt's badVerb
// path dereferences a top-level pointer to a compound type (array, slice, struct,
// or map) when it falls back to default formatting, so a slice one pointer below
// EmailAllowlist would still be reachable. A pointer to the slice pushes the
// compound value one level further, where fmt renders it as an address instead.
type allowedAddresses struct {
	folded *[]string
}

// NewEmailAllowlist validates addresses and returns the allowlist they describe.
//
// An empty or nil list returns the open allowlist. Every entry is trimmed and
// must be a plausible address: within [MaxEmailLen], exactly one "@" with a
// non-empty local part and a host part carrying a dot, and free of spaces and
// control bytes. Two entries that differ only in case are a configuration error
// rather than a silent dedupe, because a duplicate means the operator believes
// something about the list that is not true.
func NewEmailAllowlist(addresses []string) (EmailAllowlist, error) {
	if len(addresses) == 0 {
		return EmailAllowlist{}, nil
	}
	if len(addresses) > MaxAllowedEmails {
		return EmailAllowlist{}, fmt.Errorf(
			"the login allowlist carries %d addresses, the limit is %d: %w",
			len(addresses), MaxAllowedEmails, ErrInvalidConfig)
	}

	folded := make([]string, 0, len(addresses))
	for i, raw := range addresses {
		address := strings.TrimSpace(raw)
		if err := checkAllowedEmail(address, i); err != nil {
			return EmailAllowlist{}, err
		}
		lowered := foldASCII(address)
		if slices.Contains(folded, lowered) {
			return EmailAllowlist{}, fmt.Errorf(
				"the login allowlist carries entry %d twice, ignoring case: %w", i, ErrInvalidConfig)
		}
		folded = append(folded, lowered)
	}
	return EmailAllowlist{permitted: &allowedAddresses{folded: &folded}}, nil
}

// checkAllowedEmail validates one entry. The error names the entry's position and
// never the entry itself, so a malformed configuration cannot print an address
// into a start-up log.
func checkAllowedEmail(address string, index int) error {
	if address == "" {
		return fmt.Errorf("login allowlist entry %d is empty: %w", index, ErrInvalidConfig)
	}
	if len(address) > MaxEmailLen {
		return fmt.Errorf("login allowlist entry %d is longer than %d bytes: %w",
			index, MaxEmailLen, ErrInvalidConfig)
	}
	for i := range len(address) {
		if address[i] <= 0x20 || address[i] == 0x7F {
			return fmt.Errorf("login allowlist entry %d carries a space or control byte: %w",
				index, ErrInvalidConfig)
		}
	}
	local, host, found := strings.Cut(address, "@")
	if !found || local == "" || host == "" || strings.Contains(host, "@") {
		return fmt.Errorf("login allowlist entry %d is not one address with a host: %w",
			index, ErrInvalidConfig)
	}
	if !strings.Contains(host, ".") {
		return fmt.Errorf("login allowlist entry %d has a host without a dot: %w",
			index, ErrInvalidConfig)
	}
	return nil
}

// Permits reports whether email may complete a login.
//
// The open allowlist permits everything. Otherwise the comparison is over the
// trimmed, folded value: a mail domain is case-insensitive, and while a local
// part is formally case-sensitive, no Garmin login distinguishes one. Folding
// is ASCII-only (ordinary strings.ToLower applies full Unicode case folding,
// under which some non-ASCII bytes fold onto an ASCII letter — U+212A KELVIN
// SIGN folds to "k" — while the raw, unfolded value is what is actually sent to
// Garmin's login, so folding wider than ASCII could compare two values as equal
// that Garmin's own login would never treat as the same account).
func (a EmailAllowlist) Permits(email string) bool {
	if a.IsOpen() {
		return true
	}
	return slices.Contains(*a.permitted.folded, foldASCII(strings.TrimSpace(email)))
}

// foldASCII lower-cases only the ASCII letters in s, leaving every other byte
// untouched. Unlike strings.ToLower, it never applies Unicode case folding, so
// it cannot fold a non-ASCII byte onto an ASCII letter.
func foldASCII(s string) string {
	out := []byte(s)
	for i, b := range out {
		if b >= 'A' && b <= 'Z' {
			out[i] = b + ('a' - 'A')
		}
	}
	return string(out)
}

// IsOpen reports whether the allowlist restricts nothing.
func (a EmailAllowlist) IsOpen() bool {
	return a.permitted == nil || len(*a.permitted.folded) == 0
}

// Len returns how many addresses the allowlist carries. It is zero when open.
func (a EmailAllowlist) Len() int {
	if a.IsOpen() {
		return 0
	}
	return len(*a.permitted.folded)
}

// String renders the size and never an address.
func (a EmailAllowlist) String() string {
	if a.IsOpen() {
		return "login allowlist: open"
	}
	return fmt.Sprintf("login allowlist: %d addresses", a.Len())
}
