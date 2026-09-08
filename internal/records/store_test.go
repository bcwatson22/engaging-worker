package records

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

var at = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*Store, *redis.Client, *miniredis.Miniredis) {
	t.Helper()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr: server.Addr(), Protocol: 2, MaxRetries: -1,
	})
	t.Cleanup(func() { _ = client.Close() })

	return New(client, func() time.Time { return at }), client, server
}

func read(t *testing.T, client *redis.Client, artifact string) []Record {
	t.Helper()

	stored, err := client.LRange(context.Background(),
		Prefix+":"+artifact, 0, -1).Result()
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	out := make([]Record, 0, len(stored))
	for _, s := range stored {
		var r Record
		if err := json.Unmarshal([]byte(s), &r); err != nil {
			t.Fatalf("decoding %q: %v", s, err)
		}
		out = append(out, r)
	}

	return out
}

/*
The shape is fixed by engaging-service's RecordStore, which reads these.

	A renamed field does not fail — the status page silently drops the entry.
*/
func TestAddWritesTheShapeTheReaderExpects(t *testing.T) {
	store, client, _ := setup(t)

	err := store.Add(context.Background(), "cv-pdf", Record{
		Result:     "https://pub.r2.dev/billy-watson-cv.pdf",
		DurationMs: 4513, Attempts: 2, ElapsedMs: 65000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := read(t, client, "cv-pdf")
	if len(got) != 1 {
		t.Fatalf("expected one record, got %d", len(got))
	}

	want := Record{
		At: at.Format(time.RFC3339Nano), Result: "https://pub.r2.dev/billy-watson-cv.pdf",
		DurationMs: 4513, Attempts: 2, ElapsedMs: 65000,
	}
	if got[0] != want {
		t.Errorf("want %+v, got %+v", want, got[0])
	}
}

/*
Stamped here rather than taken from the caller: a record should not be able

	to claim a time of its own choosing.
*/
func TestAddStampsItsOwnTime(t *testing.T) {
	store, client, _ := setup(t)

	if err := store.Add(context.Background(), "cv-pdf",
		Record{At: "1999-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := read(t, client, "cv-pdf")[0].At; got != at.Format(time.RFC3339Nano) {
		t.Errorf("expected the store's own time, got %q", got)
	}
}

func TestAddKeepsNewestFirst(t *testing.T) {
	store, client, _ := setup(t)

	for _, r := range []string{"first", "second", "third"} {
		if err := store.Add(context.Background(), "cv-pdf", Record{Result: r}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	got := read(t, client, "cv-pdf")
	if got[0].Result != "third" {
		t.Errorf("newest should be the head, got %q", got[0].Result)
	}
}

/*
Trimmed on write, so the list cannot grow past the limit even if a render

	loop went wrong.
*/
func TestAddTrimsToTheLimit(t *testing.T) {
	store, client, _ := setup(t)

	for i := range Limit + 5 {
		if err := store.Add(context.Background(), "cv-pdf",
			Record{Attempts: i}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if got := read(t, client, "cv-pdf"); len(got) != Limit {
		t.Errorf("expected %d records, got %d", Limit, len(got))
	}
}

func TestAddKeepsArtifactsApart(t *testing.T) {
	store, client, _ := setup(t)

	_ = store.Add(context.Background(), "cv-pdf", Record{Result: "pdf"})
	_ = store.Add(context.Background(), "startup-images", Record{Result: "images"})

	if got := read(t, client, "cv-pdf"); len(got) != 1 || got[0].Result != "pdf" {
		t.Errorf("cv-pdf history: %+v", got)
	}
	if got := read(t, client, "startup-images"); len(got) != 1 {
		t.Errorf("startup-images history: %+v", got)
	}
}

func TestAddReportsAFailure(t *testing.T) {
	store, client, server := setup(t)
	_ = client.Close()
	server.Close()

	if err := store.Add(context.Background(), "cv-pdf", Record{}); err == nil {
		t.Fatal("expected an error against a closed Redis")
	}
}

func TestNewDefaultsItsClock(t *testing.T) {
	if New(nil, nil).now == nil {
		t.Error("expected a clock to be filled in")
	}
}

func TestElapsed(t *testing.T) {
	now := at

	cases := map[string]struct {
		requestedAt string
		want        int64
	}{
		"the wait between enqueue and finish": {at.Add(-65 * time.Second).Format(time.RFC3339), 65000},
		"missing":                             {"", 0},
		"unparseable":                         {"whenever", 0},
		/* Clocks disagree across machines; a negative elapsed is noise, not
		   information, and a status page showing one is worse than a zero. */
		"queued in the future": {at.Add(time.Minute).Format(time.RFC3339), 0},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Elapsed(c.requestedAt, now); got != c.want {
				t.Errorf("want %d, got %d", c.want, got)
			}
		})
	}
}
