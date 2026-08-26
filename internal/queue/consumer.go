package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Defaults, each of which is load-bearing.
const (
	// MinIdle must exceed the worst-case job: roughly 20 seconds of render
	// plus the 150-second retry ladder. Set it lower and XAUTOCLAIM reclaims
	// work another consumer is still healthily doing — which fails silently,
	// because both consumers then succeed and XPENDING returns to zero.
	DefaultMinIdle = 5 * time.Minute

	// Attempts and Backoff mirror engaging-service's ladder: five attempts
	// from 10s gives 10 + 20 + 40 + 80 = 150 seconds of patience, which is
	// what the publish race needs.
	DefaultAttempts = 5
	DefaultBackoff  = 10 * time.Second

	// ErrorFloor is how long to wait after a read that failed, so a failing
	// Redis is never retried tight.
	DefaultErrorFloor = time.Second

	// IdleFloor is how long to wait after a read that found nothing.
	//
	// This is deliberately a poll rather than XREADGROUP BLOCK. Under quota
	// pressure Upstash stops blocking — engaging-service#24 was a blocking
	// read degrading into a hot loop at ~10 reads a second — so a design that
	// depends on BLOCK behaving is a design that rebuilds that failure. A
	// deliberate interval cannot degrade: it costs one command per second
	// whatever the server does, which over the couple of minutes this worker
	// is alive per job is a rounding error against the monthly allowance.
	DefaultIdleFloor = time.Second

	// MaxReadFailures is when to stop trying. The error floor stops a failing
	// read becoming a hot loop, but on its own it would still loop forever —
	// and a worker that never returns is a machine that never stops, which is
	// the opposite of what this design is for. Giving up exits non-zero, Fly
	// restarts once on its own, and the API tier's backstop wakes it again
	// later; a Redis that is down stays a problem either way, but not a
	// billable one.
	DefaultMaxReadFailures = 10

	// DrainAfter is how long the stream must stay empty before the worker
	// gives up and exits, which is what stops the Fly machine. Long enough
	// that a second artifact queued moments later is not missed.
	DefaultDrainAfter = 30 * time.Second

	// deadLetterMax caps the dead-letter stream. BullMQ had removeOnFail; a
	// stream grows forever without being told not to.
	deadLetterMax = 500
)

// Handler processes one job. Returning an error causes a retry; running out of
// attempts sends the job to the dead-letter stream.
type Handler func(context.Context, Job) error

// Options configures a Consumer. The zero value of each falls back to the
// defaults above.
type Options struct {
	Consumer   string
	MinIdle    time.Duration
	ErrorFloor time.Duration
	IdleFloor  time.Duration
	DrainAfter time.Duration
	Attempts   int
	Backoff    time.Duration

	MaxReadFailures int

	// Sleep is injectable so tests do not wait out a 150-second ladder.
	Sleep func(time.Duration)
	// Now is injectable for the same reason.
	Now func() time.Time
}

// Consumer reads jobs from the stream until it is drained.
type Consumer struct {
	client *redis.Client
	opts   Options
}

// New builds a consumer, filling in any option left at its zero value.
func New(client *redis.Client, opts Options) *Consumer {
	if opts.MinIdle == 0 {
		opts.MinIdle = DefaultMinIdle
	}
	if opts.ErrorFloor == 0 {
		opts.ErrorFloor = DefaultErrorFloor
	}
	if opts.IdleFloor == 0 {
		opts.IdleFloor = DefaultIdleFloor
	}
	if opts.DrainAfter == 0 {
		opts.DrainAfter = DefaultDrainAfter
	}
	if opts.Attempts == 0 {
		opts.Attempts = DefaultAttempts
	}
	if opts.Backoff == 0 {
		opts.Backoff = DefaultBackoff
	}
	if opts.MaxReadFailures == 0 {
		opts.MaxReadFailures = DefaultMaxReadFailures
	}
	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Consumer == "" {
		opts.Consumer = "worker"
	}

	return &Consumer{client: client, opts: opts}
}

// EnsureGroup creates the stream and group if they are not there yet, so the
// worker can start before anything has ever been enqueued.
func (c *Consumer) EnsureGroup(ctx context.Context) error {
	err := c.client.XGroupCreateMkStream(ctx, Stream, Group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}

	return nil
}

// Run consumes until the stream has been empty for DrainAfter, then returns.
// Returning is the point: the worker exits, which is what stops the Fly
// machine, because the proxy's own autostop cannot be trusted to do it — it
// reads a machine mid-render as idle.
func (c *Consumer) Run(ctx context.Context, handle Handler) error {
	if err := c.EnsureGroup(ctx); err != nil {
		return err
	}

	lastWork := c.opts.Now()
	failures := 0

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		messages, err := c.next(ctx)
		if err != nil {
			failures++
			if failures >= c.opts.MaxReadFailures {
				return fmt.Errorf("giving up after %d failed reads: %w", failures, err)
			}

			// Never retry a failed read tight.
			slog.Warn("reading the stream failed", "err", err, "failures", failures)
			c.opts.Sleep(c.opts.ErrorFloor)

			continue
		}

		failures = 0

		if len(messages) == 0 {
			if c.opts.Now().Sub(lastWork) >= c.opts.DrainAfter {
				slog.Info("stream drained, exiting so the machine can stop")

				return nil
			}

			c.opts.Sleep(c.opts.IdleFloor)

			continue
		}

		lastWork = c.opts.Now()

		for _, m := range messages {
			c.process(ctx, m, handle)
		}
	}
}

// next takes abandoned work first, then anything new. Orphans are checked
// first deliberately: a job a crashed consumer was holding is older than
// anything waiting behind it.
func (c *Consumer) next(ctx context.Context) ([]redis.XMessage, error) {
	claimed, _, err := c.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream: Stream, Group: Group, Consumer: c.opts.Consumer,
		MinIdle: c.opts.MinIdle, Start: "0", Count: 10,
	}).Result()
	if err != nil {
		return nil, err
	}

	if len(claimed) > 0 {
		slog.Info("reclaimed abandoned work", "messages", len(claimed))

		return claimed, nil
	}

	// Block: -1 is go-redis for "do not block". Leaving it at the zero value
	// sends BLOCK 0, which blocks *forever* — the opposite of what a zero
	// reads as, and a worker that never returns.
	//
	// Not blocking is the point: see IdleFloor. The wait between polls is ours
	// rather than the server's, so a Redis that stops honouring BLOCK cannot
	// turn this into a hot loop.
	streams, err := c.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: Group, Consumer: c.opts.Consumer, Streams: []string{Stream, ">"},
		Count: 10, Block: -1,
	}).Result()

	// Nil is go-redis for "nothing waiting", which is the ordinary case for a
	// queue that runs twice a month.
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Flattened rather than indexed: only one stream is ever read, but taking
	// streams[0] would need a length check that nothing can make false, which
	// is a branch no test could ever reach.
	var messages []redis.XMessage
	for _, s := range streams {
		messages = append(messages, s.Messages...)
	}

	return messages, nil
}

// process runs a job through its retry ladder, then gives up into the
// dead-letter stream. It always acks: an un-acked failure would be reclaimed
// by the next XAUTOCLAIM and retried forever.
func (c *Consumer) process(ctx context.Context, m redis.XMessage, handle Handler) {
	job, err := Decode(m.Values)
	if err != nil {
		c.giveUp(ctx, m, err, 0)

		return
	}

	for attempt := 1; attempt <= c.opts.Attempts; attempt++ {
		err = handle(ctx, job)
		if err == nil {
			c.ack(ctx, m.ID)
			slog.Info("job finished", "id", m.ID, "job", job.Job, "attempts", attempt)

			return
		}

		slog.Warn("attempt failed", "id", m.ID, "job", job.Job,
			"attempt", attempt, "of", c.opts.Attempts, "err", err)

		if attempt < c.opts.Attempts {
			c.opts.Sleep(c.opts.Backoff * (1 << (attempt - 1)))
		}
	}

	c.giveUp(ctx, m, err, c.opts.Attempts)
}

func (c *Consumer) ack(ctx context.Context, id string) {
	if err := c.client.XAck(ctx, Stream, Group, id).Err(); err != nil {
		slog.Error("acking failed", "id", id, "err", err)
	}
}

// giveUp records why, then acks, so /status can report it and the message
// stops being reclaimed.
func (c *Consumer) giveUp(ctx context.Context, m redis.XMessage, cause error, attempts int) {
	slog.Error("giving up on a job", "id", m.ID, "attempts", attempts, "err", cause)

	payload, _ := m.Values[Field].(string)

	if err := c.client.XAdd(ctx, &redis.XAddArgs{
		Stream: DeadLetter,
		MaxLen: deadLetterMax,
		Approx: true,
		Values: map[string]any{
			Field:      payload,
			"error":    cause.Error(),
			"attempts": attempts,
			"failedAt": c.opts.Now().UTC().Format(time.RFC3339),
			"original": m.ID,
		},
	}).Err(); err != nil {
		slog.Error("could not write a dead letter", "id", m.ID, "err", err)
	}

	c.ack(ctx, m.ID)
}
