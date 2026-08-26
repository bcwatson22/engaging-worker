// Command worker renders the artifacts engaging.engineering links to.
//
// Phase 1 drives it from a flag rather than a queue: the only question at this
// stage is whether Go can reproduce the existing renders, so there is nothing
// to be gained from wiring a consumer up first.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/bcwatson22/engaging-worker/internal/config"
	"github.com/bcwatson22/engaging-worker/internal/render"
	"github.com/bcwatson22/engaging-worker/internal/storage"
)

// candidatePrefix keeps Phase 1 output away from anything the site links to.
// Phase 3 is where this becomes empty and the render takes over the real key.
const candidatePrefix = "candidate/"

func main() {
	artifact := flag.String("render", "", "render an artifact once and exit (cv-pdf)")
	prefix := flag.String("prefix", candidatePrefix, "object key prefix")
	out := flag.String("out", "", "also write the result to this local path")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.FromEnv()
	if err != nil {
		slog.Error("configuration", "err", err)
		os.Exit(1)
	}

	if *artifact != "" {
		if err := renderOnce(cfg, *artifact, *prefix, *out); err != nil {
			slog.Error("render failed", "err", err)
			os.Exit(1)
		}
		return
	}

	serve(cfg)
}

func renderOnce(cfg *config.Config, name, prefix, out string) error {
	if name != "cv-pdf" {
		return fmt.Errorf("unknown artifact %q", name)
	}

	art := render.CVPDF
	url := cfg.SiteURL + art.Path
	start := time.Now()

	browser, err := render.Launch(cfg.ChromePath)
	if err != nil {
		return err
	}
	// Always — an orphaned Chrome holds its memory for the life of the
	// container.
	defer browser.Close()

	slog.Info("rendering", "url", url)

	pdf, err := browser.PDF(url)
	if err != nil {
		return err
	}

	pages, err := render.PageCount(pdf)
	if err != nil {
		return err
	}

	slog.Info("rendered", "pages", pages, "bytes", len(pdf),
		"ms", time.Since(start).Milliseconds())

	if out != "" {
		if err := os.WriteFile(out, pdf, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", out, err)
		}
		slog.Info("wrote local copy", "path", out)
	}

	r2 := storage.New(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretAccessKey,
		cfg.R2Bucket, cfg.R2PublicBase)

	public, err := r2.Upload(context.Background(), prefix+art.Key, pdf,
		art.ContentType, art.CacheControl)
	if err != nil {
		return err
	}

	slog.Info("uploaded", "url", public)

	return nil
}

// serve exists so the platform has something to health-check, and so the Fly
// proxy has a service to wake the machine on. It does no work of its own.
func serve(cfg *config.Config) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	slog.Info("listening", "addr", addr)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
