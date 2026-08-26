package hash

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func store(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()

	server := miniredis.RunT(t)
	// RESP2: go-redis otherwise blocks in its push-notification reader against
	// a server that does not fully implement RESP3.
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), Protocol: 2})
	t.Cleanup(func() { _ = client.Close() })

	return NewStore(client), server
}

// The key format is a contract with engaging-service's HashStore. If it drifts
// the two stop seeing each other's work, and the only symptom is every render
// looking like a change forever.
func TestStoreUsesTheServiceKeyFormat(t *testing.T) {
	s, server := store(t)

	if err := s.Set(context.Background(), "billy-watson-cv.pdf", "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := server.Get("content-hash:billy-watson-cv.pdf")
	if err != nil {
		t.Fatalf("expected the key engaging-service writes: %v", err)
	}
	if got != "abc123" {
		t.Errorf("stored %q", got)
	}
}

// Nothing rendered yet is not an error — it is the first run, and treating it
// as a failure would stop the first render ever happening.
func TestStoreGetIsEmptyWhenNothingRendered(t *testing.T) {
	s, _ := store(t)

	got, err := s.Get(context.Background(), "billy-watson-cv.pdf")
	if err != nil {
		t.Fatalf("a missing key should not be an error: %v", err)
	}
	if got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestStoreRoundTrips(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()

	if err := s.Set(ctx, "startup-images", "def456"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, "startup-images")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "def456" {
		t.Errorf("want def456, got %q", got)
	}
}

func TestStoreReportsRealFailures(t *testing.T) {
	s, server := store(t)
	ctx := context.Background()
	server.SetError("READONLY the server is read only")

	if _, err := s.Get(ctx, "x"); err == nil {
		t.Error("expected Get to report a broken Redis")
	}
	if err := s.Set(ctx, "x", "y"); err == nil {
		t.Error("expected Set to report a broken Redis")
	}
}
