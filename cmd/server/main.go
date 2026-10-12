// Command server runs the Secure Intake (PII Shield) demo app.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rhapsody008/sovereign-ai-demo/internal/config"
	"github.com/rhapsody008/sovereign-ai-demo/internal/nai"
	"github.com/rhapsody008/sovereign-ai-demo/internal/server"
	"github.com/rhapsody008/sovereign-ai-demo/internal/store"
	"github.com/rhapsody008/sovereign-ai-demo/web"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	var st store.Store
	if cfg.S3Endpoint == "" {
		log.Warn("S3_ENDPOINT not set: using in-memory store (records are not shared between instances)")
		st = store.NewMemory()
	} else {
		s3, err := store.NewS3(store.S3Options{
			Endpoint: cfg.S3Endpoint, UseTLS: cfg.S3UseTLS, InsecureSkipVerify: cfg.S3InsecureSkipVerify,
			Bucket: cfg.S3Bucket, AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey, Region: cfg.S3Region,
		})
		if err != nil {
			log.Error("storage client", "err", err)
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := s3.EnsureBucket(ctx, cfg.S3Region); err != nil {
			log.Warn("could not verify or create bucket", "bucket", cfg.S3Bucket, "err", err)
		}
		cancel()
		st = s3
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := server.New(cfg, st, nai.New(cfg.NAITimeout, cfg.NAIInsecureSkipVerify), web.FS, version, log)
	seedCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	srv.SeedIfEmpty(seedCtx)
	cancel()
	srv.Start(ctx)

	hs := &http.Server{Addr: cfg.ListenAddr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr, "cluster", cfg.ClusterName, "role", cfg.ClusterRole,
			"storage", st.Backend(), "version", version)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = hs.Shutdown(shCtx)
}
