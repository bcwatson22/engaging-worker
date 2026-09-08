// Package records writes what a render produced and what it cost, for the
// status endpoint engaging-service serves.
//
// This is the third contract between the two repos, after the queue payload
// and the content-hash key. It is a Redis list per artifact, newest first,
// written here and read by that service's RecordStore — which owned both ends
// until rendering moved. Its shape is fixed by the reader: change a field name
// and the status page silently drops the entry rather than failing.
package records

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Prefix and Limit match engaging-service's RecordStore exactly.
const (
	Prefix = "render-history"

	// Renders happen roughly twice a month, so twenty entries is the best part
	// of a year — long enough to be a history, short enough that the whole
	// list can be read on every status request without paging.
	Limit = 20
)

// Record is what a render produced and what it took to get there.
//
// DurationMs is the render itself. ElapsedMs is enqueue to finish, so the
// difference between them is time spent waiting for the site to catch up:
// the publish race the content-hash check retries through. Attempts is how
// many passes that took. Recorded because the logs are the only other place
// this exists, and they go with the machine.
type Record struct {
	At         string `json:"at"`
	Result     string `json:"result"`
	DurationMs int64  `json:"durationMs"`
	Attempts   int    `json:"attempts"`
	ElapsedMs  int64  `json:"elapsedMs"`
}

// Store appends to the history.
type Store struct {
	client *redis.Client
	now    func() time.Time
}

// New builds a store. now is injected so a test can assert the timestamp.
func New(client *redis.Client, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}

	return &Store{client: client, now: now}
}

// Add pushes then trims, so the list cannot grow past the limit even if a
// render loop went wrong. Both in one pipeline: two round trips to Upstash for
// something written a couple of times a month is still one more than it needs.
//
// `at` is stamped here rather than taken from the caller, matching the service
// that used to write these — a record should not be able to claim a time of
// its own choosing.
func (s *Store) Add(ctx context.Context, artifact string, r Record) error {
	r.At = s.now().UTC().Format(time.RFC3339Nano)

	// Marshalling a struct of strings and ints cannot fail, and handling an
	// error that cannot happen would add a branch no test can reach.
	encoded, _ := json.Marshal(r)

	key := fmt.Sprintf("%s:%s", Prefix, artifact)

	pipe := s.client.TxPipeline()
	pipe.LPush(ctx, key, encoded)
	pipe.LTrim(ctx, key, 0, Limit-1)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("recording the render: %w", err)
	}

	return nil
}

// Elapsed is enqueue to now, from the payload's requestedAt. An unparseable or
// missing timestamp reports zero rather than failing the render: the artifact
// is already published by the time this is written, and a status page is not
// worth losing it for.
func Elapsed(requestedAt string, now time.Time) int64 {
	queued, err := time.Parse(time.RFC3339, requestedAt)
	if err != nil {
		return 0
	}

	elapsed := now.Sub(queued).Milliseconds()
	if elapsed < 0 {
		return 0
	}

	return elapsed
}
