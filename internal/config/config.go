// Package config parses and validates the environment once, at boot.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config is the whole of the worker's configuration. Everything is required
// except Port — there is no development stub for a credential, because a
// misconfigured deploy should fail at boot rather than on the first render
// that happens to need it.
type Config struct {
	Port    int
	SiteURL string

	R2AccountID       string
	R2AccessKeyID     string
	R2SecretAccessKey string
	R2Bucket          string
	R2PublicBase      string

	// ChromePath is empty locally, where go-rod finds its own browser, and set
	// in the image to the Chromium the package manager installed.
	ChromePath string
}

const defaultPort = 8080

// InvalidMessage prefixes the error every caller sees, so the cause is
// recognisable in a deploy log without reading the stack.
const InvalidMessage = "invalid environment configuration:"

// Load reads the environment and reports *every* problem at once. Failing on
// the first one turns a misconfigured deploy into a guessing game played one
// release at a time.
func Load(getenv func(string) string) (*Config, error) {
	var issues []string

	required := func(key string) string {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			issues = append(issues, key+" — required")
		}
		return v
	}

	requiredURL := func(key string) string {
		v := required(key)
		if v == "" {
			return v
		}
		u, err := url.Parse(v)
		if err != nil || u.Scheme == "" || u.Host == "" {
			issues = append(issues, key+" — must be an absolute URL")
			return v
		}
		// Paths are appended directly to SiteURL, so a trailing slash would
		// produce //cv and a redirect on every render.
		return strings.TrimSuffix(v, "/")
	}

	cfg := &Config{
		Port:              defaultPort,
		SiteURL:           requiredURL("SITE_URL"),
		R2AccountID:       required("R2_ACCOUNT_ID"),
		R2AccessKeyID:     required("R2_ACCESS_KEY_ID"),
		R2SecretAccessKey: required("R2_SECRET_ACCESS_KEY"),
		R2Bucket:          required("R2_BUCKET"),
		R2PublicBase:      requiredURL("R2_PUBLIC_BASE"),
		ChromePath:        strings.TrimSpace(getenv("CHROME_PATH")),
	}

	if raw := strings.TrimSpace(getenv("PORT")); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			issues = append(issues, "PORT — must be a port number")
		} else {
			cfg.Port = port
		}
	}

	if len(issues) > 0 {
		return nil, fmt.Errorf("%s %s", InvalidMessage, strings.Join(issues, "; "))
	}

	return cfg, nil
}

// FromEnv is Load against the real environment.
func FromEnv() (*Config, error) { return Load(os.Getenv) }
