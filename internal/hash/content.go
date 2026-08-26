// Package hash reproduces engaging-service's content hash, byte for byte.
//
// The two implementations are a contract: the service records the hash of what
// it last rendered, and this worker compares against it to decide whether the
// site has finished revalidating. A hash that differs by a single byte would
// make every render look like a change and every retry ladder run to
// exhaustion, so the port is deliberately literal.
package hash

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// The body is hashed, not <main>.
//
// Streaming SSR does not put the finished page inside <main>: each Suspense
// boundary first emits a fallback, and the real content arrives later as a
// sibling. Scripts are stripped because the RSC payload and chunk filenames
// change on every deploy, and <head> is excluded for the same reason.
var (
	bodyPattern   = regexp.MustCompile(`(?s)<body[^>]*>(.*)</body>`)
	scriptPattern = regexp.MustCompile(`(?s)<script.*?</script>`)

	// JavaScript's \s and Go's \s are not the same class. JS includes the
	// Unicode spaces — non-breaking space above all, which HTML is full of —
	// and Go's does not. Left as \s+ this would agree with the Nest
	// implementation on most pages and silently disagree on any page
	// containing an &nbsp;, which is the worst possible failure: rare,
	// content-dependent, and indistinguishable from the page having changed.
	whitespacePattern = regexp.MustCompile(
		"[\t\n\v\f\r \u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\ufeff]+")
)

// ErrNoBody means the fetched page had no <body>, which should be impossible
// and is worth failing on rather than hashing whatever came back.
var ErrNoBody = errors.New("could not find a <body> element to hash")

// Extract is the text the hash is taken over. Exported so a mismatch with the
// service can be diffed rather than guessed at.
func Extract(html string) (string, error) {
	m := bodyPattern.FindStringSubmatch(html)
	if m == nil {
		return "", ErrNoBody
	}

	stripped := scriptPattern.ReplaceAllString(m[1], "")

	return strings.TrimSpace(whitespacePattern.ReplaceAllString(stripped, " ")), nil
}

// Content hashes a page's markup.
func Content(html string) (string, error) {
	content, err := Extract(html)
	if err != nil {
		return "", err
	}

	return sum(content), nil
}

func sum(s string) string {
	digest := sha256.Sum256([]byte(s))
	return hex.EncodeToString(digest[:])
}

// Fetch retrieves a page and hashes it.
func Fetch(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	// Otherwise the edge can answer with exactly the version this is trying to
	// detect a change from.
	req.Header.Set("cache-control", "no-cache")

	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", fmt.Errorf("the site responded with status %d", res.StatusCode)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	return Content(string(body))
}

// Combined hashes several pages into one value, because an artifact derived
// from more than one page changes when any of them does.
func Combined(ctx context.Context, client *http.Client, urls []string) (string, error) {
	hashes := make([]string, 0, len(urls))

	for _, url := range urls {
		h, err := Fetch(ctx, client, url)
		if err != nil {
			return "", err
		}
		hashes = append(hashes, h)
	}

	return sum(strings.Join(hashes, ":")), nil
}
