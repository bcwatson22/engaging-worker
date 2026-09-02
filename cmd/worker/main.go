// Command worker renders the artifacts engaging.engineering links to.
//
// It wakes when the API tier pings it, drains the queue, and exits — which is
// what stops the Fly machine. Fly's own autostop cannot do that job: it is
// concurrency-driven, and reads a worker rendering for twenty seconds with no
// open request as idle.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/bcwatson22/engaging-worker/internal/config"
	"github.com/bcwatson22/engaging-worker/internal/hash"
	"github.com/bcwatson22/engaging-worker/internal/queue"
	"github.com/bcwatson22/engaging-worker/internal/render"
	"github.com/bcwatson22/engaging-worker/internal/storage"
	"github.com/bcwatson22/engaging-worker/internal/worker"
)

// productionPrefix is empty: this worker now writes the keys the site links
// to. It rendered to candidate/ through phases 1 to 3 while engaging-service
// still produced the real artifacts, and four publishes produced
// pixel-identical output before this changed.
//
// The hash key is derived from the same prefix, so this one value moved both:
// the worker now tracks the real artifact's last-rendered state rather than
// the candidate's. Pass -prefix candidate/ to render a comparison copy again
// without touching anything the site links to.
const productionPrefix = ""

func main() {
	artifact := flag.String("render", "", "render an artifact once and exit (cv-pdf)")
	prefix := flag.String("prefix", productionPrefix, "object key prefix")
	out := flag.String("out", "", "also write the result to this local path")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.FromEnv()
	if err != nil {
		slog.Error("configuration", "err", err)
		os.Exit(1)
	}

	if err := run(cfg, *artifact, *prefix, *out); err != nil {
		slog.Error("worker failed", "err", err)
		os.Exit(1)
	}
}

func run(cfg *config.Config, artifact, prefix, out string) error {
	if artifact != "" {
		return renderOnce(cfg, artifact, prefix, out)
	}

	return consume(cfg, prefix)
}

func redisClient(url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parsing REDIS_URL: %w", err)
	}

	// RESP2 rather than the default RESP3. Nothing here needs RESP3, and its
	// push-notification channel is one more thing for a proxied Redis to
	// implement incompletely — engaging-service already disables ioredis's
	// ready check for the same reason, Upstash's proxy not answering INFO.
	// A client that blocks in its push reader is a worker that never renders.
	opts.Protocol = 2

	return redis.NewClient(opts), nil
}

func consume(cfg *config.Config, prefix string) error {
	client, err := redisClient(cfg.RedisURL)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	// SIGTERM is how Fly asks a machine to stop. Cancelling here lets an
	// in-flight render finish its current step and leaves the message
	// unacked, so XAUTOCLAIM picks it up rather than it being lost.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go serve(ctx, cfg.Port)

	w := &worker.Worker{
		SiteURL: cfg.SiteURL,
		Prefix:  prefix,
		Store:   hash.NewStore(client),
		Uploader: storage.New(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretAccessKey,
			cfg.R2Bucket, cfg.R2PublicBase),
		Launch: func() (worker.Renderer, error) { return render.Launch(cfg.ChromePath) },
		Client: &http.Client{Timeout: 30 * time.Second},
	}

	consumer := queue.New(client, queue.Options{
		Consumer: consumerName(),
		// Named rather than inherited: zero would mean "exit the moment the
		// stream is empty", which would stop the machine between two artifacts
		// queued by the same publish.
		DrainAfter: queue.DefaultDrainAfter,
	})

	slog.Info("consuming", "stream", queue.Stream, "consumer", consumerName())

	if err := consumer.Run(ctx, w.Handle); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	return nil
}

// consumerName identifies this machine in the group's pending list, so
// XAUTOCLAIM can tell whose work was abandoned.
func consumerName() string {
	if id := os.Getenv("FLY_MACHINE_ID"); id != "" {
		return id
	}

	host, err := os.Hostname()
	if err != nil {
		return "worker"
	}

	return host
}

func renderOnce(cfg *config.Config, name, prefix, out string) error {
	client, err := redisClient(cfg.RedisURL)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	w := &worker.Worker{
		SiteURL: cfg.SiteURL,
		Prefix:  prefix,
		Store:   hash.NewStore(client),
		Uploader: storage.New(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretAccessKey,
			cfg.R2Bucket, cfg.R2PublicBase),
		Launch: func() (worker.Renderer, error) { return render.Launch(cfg.ChromePath) },
		Client: &http.Client{Timeout: 30 * time.Second},
	}

	if out != "" {
		w.Launch = writingRenderer(cfg.ChromePath, out)
	}

	// Forced: a manual render exists for changes the CMS knows nothing about,
	// which the unchanged-content check would otherwise reject.
	return w.Handle(context.Background(), queue.Job{
		V: queue.Version, Job: name, Force: true,
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// writingRenderer keeps a local copy on the way past, for comparing against
// the existing implementation.
func writingRenderer(chromePath, out string) func() (worker.Renderer, error) {
	return func() (worker.Renderer, error) {
		b, err := render.Launch(chromePath)
		if err != nil {
			return nil, err
		}

		return &localCopy{Browser: b, path: out}, nil
	}
}

type localCopy struct {
	*render.Browser
	path string
}

func (l *localCopy) PDF(url string) ([]byte, error) {
	pdf, err := l.Browser.PDF(url)
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(l.path, pdf, 0o644); err != nil {
		return nil, err
	}

	slog.Info("wrote local copy", "path", l.path)

	return pdf, nil
}

// serve exists so the Fly proxy has a service to wake the machine on, and so
// the platform has something to check. It does no work of its own.
func serve(ctx context.Context, port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()

	slog.Info("listening", "port", port)

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server stopped", "err", err)
	}
}
