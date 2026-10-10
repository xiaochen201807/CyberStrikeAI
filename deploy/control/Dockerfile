# Control-plane image only. Scanner binaries are intentionally kept in the
# separate deploy/tools Compose project; the agent-facing YAML registry is
# included so the current project does not lose its existing capabilities.
FROM golang:1.25-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/cyberstrike-ai ./cmd/server

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --create-home --uid 10001 --shell /usr/sbin/nologin cyberstrike

WORKDIR /app
COPY --from=builder /out/cyberstrike-ai /app/bin/cyberstrike-ai
COPY --from=builder /src/config.example.yaml /app/config.example.yaml
COPY --from=builder /src/web /app/web
COPY --from=builder /src/roles /app/roles
COPY --from=builder /src/roles /app/config/roles
COPY --from=builder /src/skills /app/skills
COPY --from=builder /src/skills /app/config/skills
COPY --from=builder /src/agents /app/agents
COPY --from=builder /src/agents /app/config/agents
COPY --from=builder /src/knowledge_base /app/knowledge_base
# Keep the repository's complete agent-facing tool registry in the control
# image.  These YAML files describe names, schemas and role capabilities; the
# executable itself may be provided by the host or an external tool runner.
COPY --from=builder /src/tools /app/tools
COPY --from=builder /src/tools /app/config/tools
COPY deploy/control/entrypoint.sh /app/entrypoint.sh

RUN mkdir -p /app/config /app/data /app/log /app/chat_uploads \
    && chown -R cyberstrike:cyberstrike /app

USER cyberstrike
ENV GIN_MODE=release
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -kfsS https://127.0.0.1:8080/healthz || curl -fsS http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/app/entrypoint.sh"]
