package app

import (
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

	return &App{
		server: &http.Server{
			Addr:    cfg.Server.Address,
			Handler: apiServer.Handler(),
		},
		api: apiServer,
	}, nil
}

func (a *App) Run() error {
	return a.server.ListenAndServe()
}

func (a *App) Handler() http.Handler {
	return a.api.Handler()
}
