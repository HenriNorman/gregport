package main

import (
    "context"
    "log"
    "net/http"
    "time"

    "github.com/go-chi/chi/v5"

    "github.com/HenriNorman/gregport/internal/platform/config"
    "github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/HenriNorman/gregport/internal/middleware"
	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/auth"
)

func main() {
    cfg := config.Load()

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    pool, err := database.NewPool(ctx, cfg.DSN())
    if err != nil {
        log.Fatalf("database: %v", err)
    }
    defer pool.Close()
    log.Println("connected to postgres")

    authRepo := auth.NewRepository(pool)
    authSvc := auth.NewService(authRepo)
    authHandler := auth.NewHandler(authSvc)

    r := chi.NewRouter()
    r.Use(middleware.Recovery)
    r.Use(middleware.RequestID)

    r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
        if err := pool.Ping(r.Context()); err != nil {
            httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
            return
        }
        httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
    })

    r.Route("/v1", func(r chi.Router) {
        r.Post("/auth/signup", authHandler.Signup)
    })

    log.Printf("listening on :%s", cfg.AppPort)
    log.Fatal(http.ListenAndServe(":"+cfg.AppPort, r))
}
