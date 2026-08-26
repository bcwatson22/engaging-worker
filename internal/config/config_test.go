package config

import (
	"strings"
	"testing"
)

// setup builds a getenv over a map, so a case states only what it sets.
func setup(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func valid() map[string]string {
	return map[string]string{
		"SITE_URL":             "https://www.engaging.engineering",
		"R2_ACCOUNT_ID":        "acct",
		"R2_ACCESS_KEY_ID":     "key",
		"R2_SECRET_ACCESS_KEY": "secret",
		"R2_BUCKET":            "bucket",
		"R2_PUBLIC_BASE":       "https://pub-abc.r2.dev",
	}
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(setup(valid()))
	if err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}

	if cfg.Port != defaultPort {
		t.Errorf("port: want %d, got %d", defaultPort, cfg.Port)
	}
	if cfg.SiteURL != "https://www.engaging.engineering" {
		t.Errorf("site url: got %q", cfg.SiteURL)
	}
	if cfg.ChromePath != "" {
		t.Errorf("chrome path should be empty when unset, got %q", cfg.ChromePath)
	}
}

// A trailing slash would make every render request //cv and take a redirect.
func TestLoadTrimsTrailingSlash(t *testing.T) {
	env := valid()
	env["SITE_URL"] = "https://www.engaging.engineering/"

	cfg, err := Load(setup(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.HasSuffix(cfg.SiteURL, "/") {
		t.Errorf("trailing slash not trimmed: %q", cfg.SiteURL)
	}
}

// Every problem at once: failing on the first turns a misconfigured deploy
// into a guessing game played one release at a time.
func TestLoadReportsEveryIssue(t *testing.T) {
	_, err := Load(setup(map[string]string{}))
	if err == nil {
		t.Fatal("expected an error for an empty environment")
	}

	for _, key := range []string{
		"SITE_URL", "R2_ACCOUNT_ID", "R2_ACCESS_KEY_ID",
		"R2_SECRET_ACCESS_KEY", "R2_BUCKET", "R2_PUBLIC_BASE",
	} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error should name %s: %v", key, err)
		}
	}

	if !strings.Contains(err.Error(), InvalidMessage) {
		t.Errorf("error should carry the recognisable prefix: %v", err)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{"relative site url", "SITE_URL", "/cv", "SITE_URL"},
		{"unparseable public base", "R2_PUBLIC_BASE", "not a url", "R2_PUBLIC_BASE"},
		{"non-numeric port", "PORT", "http", "PORT"},
		{"port out of range", "PORT", "70000", "PORT"},
		{"whitespace is empty", "R2_BUCKET", "   ", "R2_BUCKET"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := valid()
			env[c.key] = c.value

			if _, err := Load(setup(env)); err == nil {
				t.Fatalf("expected %s to be rejected", c.key)
			} else if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should name %s: %v", c.want, err)
			}
		})
	}
}

func TestLoadAcceptsPortAndChromePath(t *testing.T) {
	env := valid()
	env["PORT"] = "3000"
	env["CHROME_PATH"] = "/usr/bin/chromium"

	cfg, err := Load(setup(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != 3000 {
		t.Errorf("port: want 3000, got %d", cfg.Port)
	}
	if cfg.ChromePath != "/usr/bin/chromium" {
		t.Errorf("chrome path: got %q", cfg.ChromePath)
	}
}

func TestFromEnvReadsProcessEnvironment(t *testing.T) {
	// Nothing set, so this must fail — enough to prove it reads os.Getenv
	// rather than that the process happens to be configured.
	t.Setenv("SITE_URL", "")

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected an error from an unconfigured environment")
	}
}
