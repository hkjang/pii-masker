package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"pii-masker/internal/config"
	"pii-masker/internal/httpapi"
	"pii-masker/internal/jobs"
	"pii-masker/internal/mock"
	"pii-masker/internal/service"
	"pii-masker/internal/upstage"
)

type App struct {
	server *http.Server
	api    *httpapi.Server
	// stop ends the background workers that outlive a single request.
	stop context.CancelFunc
	// shutdownTimeout bounds how long a graceful stop waits for in-flight
	// requests before the remaining connections are closed.
	shutdownTimeout time.Duration
}

func New(cfg config.Config) (*App, error) {
	jobStore, err := jobs.New(cfg.Storage.RootDir)
	if err != nil {
		return nil, err
	}

	upstageClient := upstage.NewClient(cfg.Upstage)
	svc := service.New(cfg, upstageClient, jobStore)
	apiServer := httpapi.New(cfg, svc)

	if cfg.Mock.EnableEmbeddedUpstageMock {
		apiServer.Mount("/internal/mock/upstage/", http.StripPrefix("/internal/mock/upstage", mock.UpstageHandler()))
	}

	ctx, cancel := context.WithCancel(context.Background())
	svc.StartRetentionSweeper(ctx)

	return &App{
		server: &http.Server{
			Addr:    cfg.Server.Address,
			Handler: apiServer.Handler(),
			// ReadTimeout and WriteTimeout stay unset on purpose: a large upload or a
			// slow upstream call would otherwise be cut off mid-request.
			ReadHeaderTimeout: orDefault(cfg.Server.ReadHeaderTimeout, config.DefaultReadHeaderTimeout),
			IdleTimeout:       orDefault(cfg.Server.IdleTimeout, config.DefaultIdleTimeout),
		},
		api:             apiServer,
		stop:            cancel,
		shutdownTimeout: orDefault(cfg.Server.ShutdownTimeout, config.DefaultShutdownTimeout),
	}, nil
}

func orDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

// Close stops the background workers started by New.
func (a *App) Close() {
	a.stop()
}

// Run serves until ctx is cancelled and then drains gracefully.
func (a *App) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", a.server.Addr)
	if err != nil {
		return err
	}
	return a.Serve(ctx, listener)
}

// Serve accepts connections on listener until ctx is cancelled. On cancellation
// it stops accepting new connections, gives the requests already in flight up to
// shutdownTimeout to finish, and only then stops the background workers.
func (a *App) Serve(ctx context.Context, listener net.Listener) error {
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- a.server.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		a.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()
	err := a.server.Shutdown(shutdownCtx)
	a.Close()
	<-serveErr
	return err
}

func (a *App) Handler() http.Handler {
	return a.api.Handler()
}
