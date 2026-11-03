# syntax=docker/dockerfile:1

FROM golang:1.27.2-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -buildvcs=false \
    -ldflags="-s -w" \
    -o /out/gregport-api ./cmd/api

FROM alpine:3.24

RUN addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app -s /sbin/nologin app

COPY --from=build /out/gregport-api /usr/local/bin/gregport-api

USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/gregport-api"]
