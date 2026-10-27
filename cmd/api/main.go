package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"

	"github.com/HenriNorman/gregport/internal/auth"
	"github.com/HenriNorman/gregport/internal/middleware"
	"github.com/HenriNorman/gregport/internal/platform/config"
	"github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/tickets"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file, relying on real env vars")
	}
	cfg := config.Load()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := database.NewPool(ctx, cfg.DSN())
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()
	log.Println("connected to postgres")

	// auth wiring
	tm := auth.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	authRepo := auth.NewRepository(pool)
	authSvc := auth.NewService(authRepo, tm)
	authHandler := auth.NewHandler(authSvc)

	// tickets wiring
	ticketsRepo := tickets.NewRepository(pool)
	ticketsSvc := tickets.NewService(ticketsRepo)
	ticketsHandler := tickets.NewHandler(ticketsSvc)

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
		// public
		r.Post("/auth/signup", authHandler.Signup)
		r.Post("/auth/login", authHandler.Login)
		r.Post("/auth/refresh", authHandler.Refresh)
		r.Post("/auth/logout", authHandler.Logout)

		// protected
		r.Group(func(r chi.Router) {
			r.Use(auth.Authenticate(tm))

			r.Get("/me", authHandler.Me)

			r.Route("/tickets", func(r chi.Router) {
				r.Post("/", ticketsHandler.Create)
				r.Get("/", ticketsHandler.List)
				r.Get("/{number}", ticketsHandler.Get)
				r.Patch("/{number}", ticketsHandler.Update)

				r.With(auth.RequireRole("owner", "admin")).
					Delete("/{number}", ticketsHandler.Delete)

				r.Post("/{number}/comments", ticketsHandler.CreateComment)
				r.Get("/{number}/comments", ticketsHandler.ListComments)
			})
		})
	})

	log.Printf("listening on :%s", cfg.AppPort)
	log.Fatal(http.ListenAndServe(":"+cfg.AppPort, r))
}
