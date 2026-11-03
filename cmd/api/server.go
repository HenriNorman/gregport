package main

import (
	"net/http"

	"github.com/HenriNorman/gregport/internal/platform/config"
)

func newServer(cfg config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + cfg.AppPort,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ServerReadHeaderTimeout,
		ReadTimeout:       cfg.ServerReadTimeout,
		WriteTimeout:      cfg.ServerWriteTimeout,
		IdleTimeout:       cfg.ServerIdleTimeout,
	}
}
