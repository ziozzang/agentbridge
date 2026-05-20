# syntax=docker/dockerfile:1.7

# Multi-stage Dockerfile for agentbridge.
#
# Build:    docker build -t agentbridge .
# Run:      docker run --rm -i \
#             -e Z_AI_API_KEY="<your-key>" \
#             -v "$PWD":/workspace -w /workspace \
#             agentbridge
#
# Speak ACP over the container's stdio (this is what an ACP-compliant IDE
# expects). The agent writes session files into /home/agent/.local/state by
# default; mount a volume there to persist them across runs.

ARG GO_VERSION=1.25
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download || true
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/agentbridge ./cmd/agentbridge

FROM alpine:3.20
# `sh` is required by run_command; ca-certificates lets the Z.AI HTTPS
# endpoints validate.
RUN apk add --no-cache ca-certificates
RUN addgroup -S agent && adduser -S agent -G agent
WORKDIR /home/agent
USER agent
COPY --from=build /out/agentbridge /usr/local/bin/agentbridge
ENV AGENTBRIDGE_SESSION_DIR=/home/agent/.local/state/agentbridge/sessions \
    XDG_CONFIG_HOME=/home/agent/.config
ENTRYPOINT ["/usr/local/bin/agentbridge"]
