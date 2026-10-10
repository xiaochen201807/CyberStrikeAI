# One image per logical family.  The family runtime owns the scanner
# dependencies; the Go MCP bridge remains the same small adapter layer for
# every family.  Keeping TOOL_FAMILY as a build argument makes the Dockerfile
# reusable while ensuring each resulting image has a finite, auditable scope.
ARG TOOL_BASE_IMAGE=kalilinux/kali-rolling@sha256:c717f201f29a7e0a9126c0d51bd08aa7194ac82f53c57314339182f92b0b1585
ARG TOOL_FAMILY

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

ARG TOOL_FAMILY
ENV TOOL_FAMILY=${TOOL_FAMILY}
ENV TOOL_DEFINITIONS_DIR=/etc/cyberstrike/tools
ENV TOOL_CATALOG=/etc/cyberstrike/catalog.yaml
ENV TOOL_PRECHECK=1
ENV HOME=/tmp

COPY deploy/tools/runtime/install-family.sh /usr/local/sbin/install-family.sh
RUN chmod 0755 /usr/local/sbin/install-family.sh \
    && /usr/local/sbin/install-family.sh "$TOOL_FAMILY"

COPY --from=builder /out/tool-mcp /usr/local/bin/tool-mcp
COPY --from=builder /out/toolcatalog /usr/local/bin/toolcatalog
COPY deploy/tools/catalog.yaml /etc/cyberstrike/catalog.yaml
COPY tools /etc/cyberstrike/tools

# Fail the image build when any enabled command in this family is absent.
# This is the key difference from the former generic Debian compatibility
# image, which only mounted YAML definitions and deferred missing binaries.
RUN /usr/local/bin/toolcatalog doctor \
      --tools-dir /etc/cyberstrike/tools \
      --family "$TOOL_FAMILY" \
      --strict

ENTRYPOINT ["/usr/local/bin/tool-mcp"]
