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

//	@title			gregport API
//	@version		1.0.0
//	@description	Multi-tenant support ticketing SaaS.
//	@description
//	@description	## Authentication
//	@description	All endpoints except `/v1/auth/*` and `/healthz` require `Authorization: Bearer <access_token>`.
//	@description	The access token is a JWT containing `sub` (user id), `org` (org id), and `role`.
//	@description	Missing, expired, or invalid tokens return 401.
//	@description
//	@description	## Authorization
//	@description	Roles are `owner`, `admin`, `agent`. Teams and macro mutations require `owner` or `admin`.
//	@description	Only `DELETE /v1/tickets/{number}` is role-gated among ticket operations (owner/admin).
//	@description	Internal notes on comments require an agent role or above.
//	@description
//	@description	## Tenant isolation
//	@description	`org_id` comes only from the token, never from the URL, body, or query string.
//	@description	A ticket in another org returns 404, never 403 — the API never confirms existence in someone else's tenant.
//	@host			localhost:8080
//	@BasePath		/
//	@schemes		http
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
//	@description				Type "Bearer" followed by a space and the access token.
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
				r.Post("/{number}/assignment", ticketsHandler.Assign)
				r.Put("/{number}/tags/{tag}", ticketsHandler.AttachTag)
				r.Delete("/{number}/tags/{tag}", ticketsHandler.DetachTag)
				r.Post("/{number}/comments", ticketsHandler.CreateComment)
				r.Get("/{number}/comments", ticketsHandler.ListComments)
			})

			r.Route("/teams", func(r chi.Router) {
				r.With(auth.RequireRole("owner", "admin")).Post("/", ticketsHandler.CreateTeam)
				r.Get("/", ticketsHandler.ListTeams)
				r.With(auth.RequireRole("owner", "admin")).Patch("/{id}", ticketsHandler.UpdateTeam)
				r.With(auth.RequireRole("owner", "admin")).Delete("/{id}", ticketsHandler.DeleteTeam)
				r.With(auth.RequireRole("owner", "admin")).Put("/{id}/members/{userId}", ticketsHandler.AddTeamMember)
				r.With(auth.RequireRole("owner", "admin")).Delete("/{id}/members/{userId}", ticketsHandler.RemoveTeamMember)
			})
			r.Route("/tags", func(r chi.Router) {
				r.Post("/", ticketsHandler.CreateTag)
				r.Get("/", ticketsHandler.ListTags)
			})
		})
	})

	log.Printf("listening on :%s", cfg.AppPort)
	log.Fatal(http.ListenAndServe(":"+cfg.AppPort, r))
}
