# gregport

A multi-tenant support ticketing SaaS — organizations, users, memberships,
tickets, comments, SLA policies, notifications, and webhooks.

## Status

Slice 1: signup (in progress). DB schema applied; HTTP boundary layer in place.

## Stack

Go 1.27, chi, pgx/v5, PostgreSQL 16 (Docker).

## Running locally

```bash
cp .env.example .env      # fill in real values
docker compose up -d --wait
go run cmd/api/main.go
curl localhost:8080/healthz
