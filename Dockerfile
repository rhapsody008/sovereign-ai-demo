# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27.1

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w" -o /out/mocknai ./cmd/mocknai

FROM gcr.io/distroless/static-debian12:nonroot AS mocknai
LABEL org.opencontainers.image.source=https://github.com/rhapsody008/sovereign-ai-demo \
      org.opencontainers.image.title=secure-intake-mocknai \
      org.opencontainers.image.description="Mock OpenAI-compatible endpoint for the Secure Intake demo"
COPY --from=build /out/mocknai /mocknai
USER nonroot
EXPOSE 9090
ENTRYPOINT ["/mocknai"]

# debug-nonroot = distroless + busybox, so `kubectl exec -it <pod> -- /busybox/sh` works.
FROM gcr.io/distroless/static-debian12:debug-nonroot AS app
ARG VERSION=dev
LABEL org.opencontainers.image.source=https://github.com/rhapsody008/sovereign-ai-demo \
      org.opencontainers.image.title=secure-intake \
      org.opencontainers.image.description="Secure Intake (PII Shield) - Sovereign AI demo" \
      org.opencontainers.image.version=${VERSION}
COPY --from=build /out/server /server
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
