# Build the small MCP bridge from this repository, then layer it onto the
# scanner image selected by the Compose service. Each resulting image contains
# exactly one scanner and its fixed MCP adapter.
ARG TOOL_BASE_IMAGE=debian:bookworm-slim
FROM golang:1.25-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY cmd/tool-mcp ./cmd/tool-mcp
COPY cmd/toolcatalog ./cmd/toolcatalog
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/tool-mcp ./cmd/tool-mcp \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/toolcatalog ./cmd/toolcatalog

FROM ${TOOL_BASE_IMAGE}

COPY --from=builder /out/tool-mcp /usr/local/bin/tool-mcp
COPY --from=builder /out/toolcatalog /usr/local/bin/toolcatalog
COPY deploy/tools/catalog.yaml /etc/cyberstrike/catalog.yaml

ENTRYPOINT ["/usr/local/bin/tool-mcp"]
