package app

import (
	"context"
	"net/http"

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
		},
		api:  apiServer,
		stop: cancel,
	}, nil
}

// Close stops the background workers started by New.
func (a *App) Close() {
	a.stop()
}

func (a *App) Run() error {
	return a.server.ListenAndServe()
}

func (a *App) Handler() http.Handler {
	return a.api.Handler()
}
