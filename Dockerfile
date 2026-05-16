# syntax=docker/dockerfile:1.7

# Multi-stage Dockerfile for glm-acp-agent.
#
# Build:    docker build -t glm-acp-agent .
# Run:      docker run --rm -i \
#             -e Z_AI_API_KEY="<your-key>" \
#             -v "$PWD":/workspace -w /workspace \
#             glm-acp-agent
#
# Speak ACP over the container's stdio (this is what an ACP-compliant IDE
# expects). The agent writes session files into /home/agent/.local/state by
# default; mount a volume there to persist them across runs.

ARG GO_VERSION=1.24
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download || true
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/glm-acp-agent ./cmd/glm-acp-agent

FROM alpine:3.20
# `sh` is required by run_command; ca-certificates lets the Z.AI HTTPS
# endpoints validate.
RUN apk add --no-cache ca-certificates
RUN addgroup -S agent && adduser -S agent -G agent
WORKDIR /home/agent
USER agent
COPY --from=build /out/glm-acp-agent /usr/local/bin/glm-acp-agent
ENV ACP_GLM_SESSION_DIR=/home/agent/.local/state/glm-acp-agent/sessions \
    XDG_CONFIG_HOME=/home/agent/.config
ENTRYPOINT ["/usr/local/bin/glm-acp-agent"]
