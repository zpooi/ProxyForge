# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.25

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
RUN apk add --no-cache nodejs
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN node frontend/scripts/build.mjs
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/proxyforge ./backend/cmd/proxyforge

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
ENV DB_PATH=/data/data.db \
    PROJECT_ROOT=/app \
    LISTEN_ADDR=:7800
VOLUME ["/data"]
COPY --from=build /out/proxyforge /app/proxyforge
EXPOSE 7800 7843
ENTRYPOINT ["/app/proxyforge"]
