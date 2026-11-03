package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"

	"github.com/HenriNorman/gregport/internal/auth"
	"github.com/HenriNorman/gregport/internal/middleware"
	"github.com/HenriNorman/gregport/internal/platform/config"
	"github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/HenriNorman/gregport/internal/platform/health"
	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/tickets"
)

// @title			gregport API
// @version		1.0.0
// @description	Multi-tenant support ticketing SaaS.
// @description
// @description	## Authentication
// @description	All endpoints except `/v1/auth/*`, `/healthz` and `/readyz` require `Authorization: Bearer <access_token>`.
// @description	The access token is a JWT containing `sub` (user id), `org` (org id), and `role`.
// @description	Missing, expired, or invalid tokens return 401.
// @description
// @description	## Authorization
// @description	Roles are `owner`, `admin`, `agent`. Teams and macro mutations require `owner` or `admin`.
// @description	Only `DELETE /v1/tickets/{number}` is role-gated among ticket operations (owner/admin).
// @description	Internal notes on comments require an agent role or above.
// @description
// @description	## Tenant isolation
// @description	`org_id` comes only from the token, never from the URL, body, or query string.
// @description	A ticket in another org returns 404, never 403 — the API never confirms existence in someone else's tenant.
// @host			localhost:8080
// @BasePath		/
// @schemes		http
// @securityDefinitions.apikey	BearerAuth
// @in							header
// @name						Authorization
// @description				Type "Bearer" followed by a space and the access token.
func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file, relying on real env vars")
	}
	cfg := config.Load()

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		log.Fatalf("logging: %v", err)
	}
	logging.SetDefault(logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := database.NewPool(ctx, cfg.DSN())
	if err != nil {
		logger.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	logger.Info("connected to postgres")

	// auth wiring
	tm := auth.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	authRepo := auth.NewRepository(pool)
	authSvc := auth.NewService(authRepo, tm)
	authHandler := auth.NewHandler(authSvc)

	// tickets wiring
	ticketsRepo := tickets.NewRepository(pool)
	ticketsSvc := tickets.NewService(ticketsRepo, tickets.NewInvitationTokenManager(7*24*time.Hour))
	ticketsHandler := tickets.NewHandler(ticketsSvc)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.WithLogger(logger))
	r.Use(middleware.RequestLogger())
	r.Use(middleware.Recovery)
	r.Use(middleware.Timeout(cfg.RequestTimeout))

	r.Get("/healthz", health.Live)
	r.Get("/readyz", health.Ready(pool))

	r.Route("/v1", func(r chi.Router) {
		// public
		r.Post("/auth/signup", authHandler.Signup)
		r.Post("/auth/login", authHandler.Login)
		r.Post("/auth/refresh", authHandler.Refresh)
		r.Post("/auth/logout", authHandler.Logout)
		r.Post("/invitations/accept", ticketsHandler.AcceptInvitation)

		// protected
		r.Group(func(r chi.Router) {
			r.Use(auth.Authenticate(tm, ticketsSvc.AuthenticateAPIKey))

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
				r.Put("/{number}/watchers/{userId}", ticketsHandler.AddWatcher)
				r.Delete("/{number}/watchers/{userId}", ticketsHandler.RemoveWatcher)
				r.Get("/{number}/events", ticketsHandler.ListEvents)
				r.Post("/{number}/comments", ticketsHandler.CreateComment)
				r.Post("/{number}/merge", ticketsHandler.Merge)
				r.Get("/{number}/comments", ticketsHandler.ListComments)
				r.Post("/{number}/macros/{id}", ticketsHandler.ApplyMacro)
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
			r.Route("/macros", func(r chi.Router) {
				r.With(auth.RequireRole("owner", "admin")).Post("/", ticketsHandler.CreateMacro)
				r.Get("/", ticketsHandler.ListMacros)
				r.With(auth.RequireRole("owner", "admin")).Patch("/{id}", ticketsHandler.UpdateMacro)
				r.With(auth.RequireRole("owner", "admin")).Delete("/{id}", ticketsHandler.DeleteMacro)
			})
			r.Route("/invitations", func(r chi.Router) {
				r.With(auth.RequireRole("owner", "admin")).Post("/", ticketsHandler.CreateInvitation)
				r.With(auth.RequireRole("owner", "admin")).Get("/", ticketsHandler.ListInvitations)
				r.With(auth.RequireRole("owner", "admin")).Delete("/{id}", ticketsHandler.DeleteInvitation)
			})
			r.Route("/members", func(r chi.Router) {
				r.Get("/", ticketsHandler.ListMembers)
				r.With(auth.RequireRole("owner", "admin")).Patch("/{userId}", ticketsHandler.UpdateMemberRole)
				r.With(auth.RequireRole("owner", "admin")).Delete("/{userId}", ticketsHandler.RemoveMember)
			})
			r.Route("/api-keys", func(r chi.Router) {
				r.With(auth.RequireRole("owner", "admin")).Post("/", ticketsHandler.CreateAPIKey)
				r.With(auth.RequireRole("owner", "admin")).Get("/", ticketsHandler.ListAPIKeys)
				r.With(auth.RequireRole("owner", "admin")).Delete("/{id}", ticketsHandler.RevokeAPIKey)
			})
		})
	})

	srv := newServer(cfg, r)

	logger.Info("listening", "port", cfg.AppPort)
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
