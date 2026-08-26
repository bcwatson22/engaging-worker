// Package queue consumes render jobs from a Redis Stream.
//
// The stream replaces BullMQ, which stopped being an implementation detail the
// moment the two ends were different languages: its payloads and retry
// bookkeeping live in Redis keys whose layout is undocumented and free to
// change in a minor release. A stream with a consumer group gives durability,
// at-least-once delivery and orphan recovery explicitly, over a payload both
// ends can read.
package queue

import (
	"encoding/json"
	"fmt"
)

// Version is the payload version. Both repos must agree on it; a message
// carrying anything else is dead-lettered rather than guessed at, which is the
// whole reason for versioning it.
const Version = 1

// Names of the stream, its consumer group, and where give-ups go.
const (
	Stream     = "render"
	Group      = "workers"
	DeadLetter = "render:dead"

	// Field holding the JSON payload within a stream entry.
	Field = "payload"
)

// Job is the contract, documented in both repos.
type Job struct {
	V           int    `json:"v"`
	Job         string `json:"job"`
	ContentHash string `json:"contentHash"`
	RequestedAt string `json:"requestedAt"`
	Force       bool   `json:"force"`
}

// ErrVersion means the producer is speaking a version this worker does not
// understand.
type ErrVersion struct{ Got int }

func (e ErrVersion) Error() string {
	return fmt.Sprintf("unsupported payload version %d, expected %d", e.Got, Version)
}

// Decode reads a job out of a stream entry's fields.
func Decode(values map[string]any) (Job, error) {
	raw, ok := values[Field].(string)
	if !ok {
		return Job{}, fmt.Errorf("stream entry has no %q field", Field)
	}

	var job Job
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		return Job{}, fmt.Errorf("unreadable payload: %w", err)
	}

	if job.V != Version {
		return Job{}, ErrVersion{Got: job.V}
	}

	return job, nil
}

// Encode is the same contract in the other direction. The worker only writes
// dead letters, but keeping both halves together is what stops them drifting.
//
// No error return: Job is strings, an int and a bool, and json.Marshal only
// fails on channels, cycles and NaN. An error nobody can trigger is a branch
// nobody can test.
func Encode(job Job) string {
	b, _ := json.Marshal(job)

	return string(b)
}
