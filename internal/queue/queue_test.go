package queue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

var errHandler = errors.New("render failed")

// setup gives a consumer over an in-memory Redis, with time under the test's
// control so a 150-second retry ladder costs nothing to exercise.
func setup(t *testing.T, options func(*Options)) (*Consumer, *redis.Client, *miniredis.Miniredis) {
	t.Helper()

	server := miniredis.RunT(t)
	// RESP2, matching the production client: go-redis negotiates RESP3 by
	// default and then blocks in its push-notification reader against a server
	// that does not fully implement it.
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), Protocol: 2})
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now()
	opts := Options{
		Consumer:   "test",
		MinIdle:    0,
		ErrorFloor: time.Millisecond,
		IdleFloor:  time.Millisecond,
		DrainAfter: time.Minute,
		Sleep:      func(d time.Duration) { now = now.Add(d) },
		Now:        func() time.Time { return now },
	}

	if options != nil {
		options(&opts)
	}

	return New(client, opts), client, server
}

func enqueue(t *testing.T, client *redis.Client, payload string) string {
	t.Helper()

	id, err := client.XAdd(context.Background(), &redis.XAddArgs{
		Stream: Stream, Values: map[string]any{Field: payload},
	}).Result()
	if err != nil {
		t.Fatalf("enqueueing: %v", err)
	}

	return id
}

func validPayload() string {
	return `{"v":1,"job":"cv-pdf","contentHash":"abc","requestedAt":"2026-08-26T00:00:00Z","force":false}`
}

func TestDecode(t *testing.T) {
	t.Run("a valid payload", func(t *testing.T) {
		job, err := Decode(map[string]any{Field: validPayload()})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if job.Job != "cv-pdf" || job.V != Version || job.ContentHash != "abc" {
			t.Errorf("decoded wrongly: %+v", job)
		}
	})

	t.Run("no payload field", func(t *testing.T) {
		if _, err := Decode(map[string]any{"other": "x"}); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("unreadable JSON", func(t *testing.T) {
		if _, err := Decode(map[string]any{Field: "{not json"}); err == nil {
			t.Fatal("expected an error")
		}
	})

	// Versioning only earns its place if an unknown version is refused rather
	// than interpreted optimistically.
	t.Run("a version this worker does not speak", func(t *testing.T) {
		_, err := Decode(map[string]any{Field: `{"v":99,"job":"cv-pdf"}`})

		var ve ErrVersion
		if !errors.As(err, &ve) {
			t.Fatalf("want ErrVersion, got %v", err)
		}
		if ve.Got != 99 || !strings.Contains(err.Error(), "99") {
			t.Errorf("error should name the version it got: %v", err)
		}
	})
}

func TestEncode(t *testing.T) {
	job, err := Decode(map[string]any{Field: Encode(Job{V: Version, Job: "cv-pdf"})})
	if err != nil {
		t.Fatalf("should round-trip: %v", err)
	}
	if job.Job != "cv-pdf" {
		t.Errorf("round-tripped wrongly: %+v", job)
	}
}

func TestNewFillsInDefaults(t *testing.T) {
	c := New(nil, Options{})

	if c.opts.MinIdle != DefaultMinIdle || c.opts.Attempts != DefaultAttempts ||
		c.opts.Backoff != DefaultBackoff || c.opts.ErrorFloor != DefaultErrorFloor ||
		c.opts.DrainAfter != DefaultDrainAfter || c.opts.IdleFloor != DefaultIdleFloor {
		t.Errorf("defaults not applied: %+v", c.opts)
	}
	if c.opts.Sleep == nil || c.opts.Now == nil || c.opts.Consumer == "" {
		t.Error("expected sleep, clock and consumer name to be filled in")
	}
}

// MinIdle has to outlast the worst-case job or XAUTOCLAIM takes work another
// consumer is still doing — and that failure is silent, because both then
// succeed and XPENDING returns to zero.
func TestMinIdleOutlastsTheRetryLadder(t *testing.T) {
	ladder := time.Duration(0)
	for attempt := 1; attempt < DefaultAttempts; attempt++ {
		ladder += DefaultBackoff * (1 << (attempt - 1))
	}

	if DefaultMinIdle <= ladder {
		t.Errorf("min-idle %s must exceed the %s ladder plus a render", DefaultMinIdle, ladder)
	}
}

func TestEnsureGroupIsRepeatable(t *testing.T) {
	c, _, _ := setup(t, nil)

	for range 2 {
		if err := c.EnsureGroup(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestEnsureGroupReportsRealFailures(t *testing.T) {
	c, _, server := setup(t, nil)
	server.Close()

	if err := c.EnsureGroup(context.Background()); err == nil {
		t.Fatal("expected an error against a closed server")
	}
}

func TestRunProcessesAndAcks(t *testing.T) {
	c, client, _ := setup(t, nil)
	enqueue(t, client, validPayload())

	var seen []Job
	c.opts.DrainAfter = 0 // exit as soon as there is nothing left

	if err := c.Run(context.Background(), func(_ context.Context, j Job) error {
		seen = append(seen, j)

		return nil
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(seen) != 1 || seen[0].Job != "cv-pdf" {
		t.Fatalf("expected one cv-pdf job, got %+v", seen)
	}

	pending, err := client.XPending(context.Background(), Stream, Group).Result()
	if err != nil {
		t.Fatalf("reading pending: %v", err)
	}
	if pending.Count != 0 {
		t.Errorf("a finished job should be acked, %d still pending", pending.Count)
	}
}

// Exiting is the point: it is what stops the Fly machine.
func TestRunExitsOnceDrained(t *testing.T) {
	c, _, _ := setup(t, func(o *Options) { o.DrainAfter = 0 })

	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), func(context.Context, Job) error { return nil }) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expected the consumer to exit when the stream was empty")
	}
}

func TestRunRetriesThenDeadLetters(t *testing.T) {
	c, client, _ := setup(t, func(o *Options) { o.DrainAfter = 0; o.Attempts = 3 })
	enqueue(t, client, validPayload())

	attempts := 0
	if err := c.Run(context.Background(), func(context.Context, Job) error {
		attempts++

		return errHandler
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}

	dead, err := client.XRange(context.Background(), DeadLetter, "-", "+").Result()
	if err != nil || len(dead) != 1 {
		t.Fatalf("expected one dead letter, got %d (%v)", len(dead), err)
	}
	if !strings.Contains(dead[0].Values["error"].(string), "render failed") {
		t.Errorf("dead letter should record why: %v", dead[0].Values)
	}

	// Acked even though it failed: otherwise XAUTOCLAIM reclaims it forever.
	pending, _ := client.XPending(context.Background(), Stream, Group).Result()
	if pending.Count != 0 {
		t.Errorf("a given-up job should still be acked, %d pending", pending.Count)
	}
}

func TestRunDeadLettersAnUnreadablePayload(t *testing.T) {
	c, client, _ := setup(t, func(o *Options) { o.DrainAfter = 0 })
	enqueue(t, client, "{not json")

	called := false
	if err := c.Run(context.Background(), func(context.Context, Job) error {
		called = true

		return nil
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if called {
		t.Error("a payload that cannot be decoded should never reach the handler")
	}

	dead, _ := client.XRange(context.Background(), DeadLetter, "-", "+").Result()
	if len(dead) != 1 {
		t.Fatalf("expected one dead letter, got %d", len(dead))
	}
}

// The entire justification for a queue: work a crashed consumer was holding
// has to come back.
func TestRunReclaimsAbandonedWork(t *testing.T) {
	c, client, _ := setup(t, func(o *Options) { o.DrainAfter = 0 })
	// Set after construction: New reads a zero MinIdle as "unset" and fills in
	// the five-minute default, which nothing in a test is old enough to meet.
	c.opts.MinIdle = 0
	ctx := context.Background()

	if err := c.EnsureGroup(ctx); err != nil {
		t.Fatalf("group: %v", err)
	}
	enqueue(t, client, validPayload())

	// A different consumer takes it and never acks — a crash mid-render.
	if _, err := client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: Group, Consumer: "crashed", Streams: []string{Stream, ">"}, Count: 1,
	}).Result(); err != nil {
		t.Fatalf("simulating a crash: %v", err)
	}

	seen := 0
	if err := c.Run(ctx, func(context.Context, Job) error {
		seen++

		return nil
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if seen != 1 {
		t.Errorf("expected the abandoned job to be reclaimed, saw %d", seen)
	}
}

// The defence against engaging-service#24: a read that fails must never be
// retried tight.
func TestRunFloorsFailingReads(t *testing.T) {
	c, _, server := setup(t, nil)
	ctx, cancel := context.WithCancel(context.Background())

	// Break Redis only once Run is under way, so the failure lands on a read
	// rather than on the group creation that precedes it.
	passes := 0
	now := time.Now()
	c.opts.Now = func() time.Time {
		passes++
		if passes == 2 {
			server.SetError("LOADING redis is loading the dataset in memory")
		}

		return now
	}

	c.opts.MaxReadFailures = 100

	floors := 0
	c.opts.Sleep = func(d time.Duration) {
		now = now.Add(d)
		if d != c.opts.ErrorFloor {
			return
		}

		floors++
		if floors >= 3 {
			cancel()
		}
	}

	if err := c.Run(ctx, func(context.Context, Job) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if floors < 3 {
		t.Errorf("expected the floor between failed reads, got %d", floors)
	}
}

// Acking and dead-lettering both happen after the work is done, so a Redis
// that fails at that moment must be logged rather than crash the worker.
func TestRunSurvivesRedisFailingAfterTheWork(t *testing.T) {
	c, client, server := setup(t, func(o *Options) { o.DrainAfter = 0 })
	enqueue(t, client, validPayload())

	handled := 0
	err := c.Run(context.Background(), func(context.Context, Job) error {
		handled++
		server.SetError("READONLY the server is read only")

		return nil
	})

	// The job itself is done; the run ends only because Redis stayed broken.
	if handled != 1 {
		t.Errorf("expected the job to be handled once, got %d", handled)
	}
	if err == nil || !strings.Contains(err.Error(), "failed reads") {
		t.Errorf("expected it to give up on a broken Redis, got %v", err)
	}
}

func TestRunSurvivesAFailedDeadLetter(t *testing.T) {
	c, client, server := setup(t, func(o *Options) { o.DrainAfter = 0; o.Attempts = 1 })
	enqueue(t, client, validPayload())

	err := c.Run(context.Background(), func(context.Context, Job) error {
		server.SetError("READONLY the server is read only")

		return errHandler
	})

	if err == nil || !strings.Contains(err.Error(), "failed reads") {
		t.Errorf("expected it to give up on a broken Redis, got %v", err)
	}
}

func TestRunStopsOnACancelledContext(t *testing.T) {
	c, _, _ := setup(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.Run(ctx, func(context.Context, Job) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// failOn makes one command fail while the rest of Redis keeps working, which
// is the only way to reach the read path's error branch: XAUTOCLAIM runs
// first, so breaking the whole server never gets that far.
type failOn struct {
	command string
	err     error
}

func (f failOn) DialHook(next redis.DialHook) redis.DialHook { return next }

func (f failOn) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if strings.EqualFold(cmd.Name(), f.command) {
			cmd.SetErr(f.err)

			return f.err
		}

		return next(ctx, cmd)
	}
}

func (f failOn) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestRunReportsAFailingRead(t *testing.T) {
	c, client, _ := setup(t, func(o *Options) { o.MaxReadFailures = 2 })
	client.AddHook(failOn{command: "xreadgroup", err: errors.New("read refused")})

	err := c.Run(context.Background(), func(context.Context, Job) error { return nil })

	if err == nil || !strings.Contains(err.Error(), "read refused") {
		t.Fatalf("expected the read failure to surface, got %v", err)
	}
}
