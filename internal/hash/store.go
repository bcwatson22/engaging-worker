package hash

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// StorePrefix must match engaging-service's HashStore. It is the second
// cross-language contract in this system and the quieter of the two: the queue
// payload is versioned and validated, whereas a key-format drift here just
// makes every render look like a change forever.
const StorePrefix = "content-hash"

// Store records the hash of what was last rendered, per artifact.
type Store struct{ client *redis.Client }

func NewStore(client *redis.Client) *Store { return &Store{client: client} }

func key(artifact string) string { return fmt.Sprintf("%s:%s", StorePrefix, artifact) }

// Get returns the last recorded hash, or empty if nothing has been rendered
// yet. Nothing rendered is not an error: it is the first run.
func (s *Store) Get(ctx context.Context, artifact string) (string, error) {
	v, err := s.client.Get(ctx, key(artifact)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	return v, nil
}

// Set records a hash. Called only after a successful upload, so a failed
// render is retried against the same previous hash rather than being treated
// as done.
func (s *Store) Set(ctx context.Context, artifact, hash string) error {
	return s.client.Set(ctx, key(artifact), hash, 0).Err()
}
