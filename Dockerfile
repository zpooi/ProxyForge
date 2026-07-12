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
# Embed both Linux agent builds so a fresh checkout can enroll amd64/arm64 VPSs
# without relying on ignored binaries already present in the build context.
RUN mkdir -p backend/internal/agentdist/dist \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o backend/internal/agentdist/dist/pfagent-linux-amd64 ./backend/cmd/pfagent \
    && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o backend/internal/agentdist/dist/pfagent-linux-arm64 ./backend/cmd/pfagent
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
