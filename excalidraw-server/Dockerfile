FROM --platform=${BUILDPLATFORM} golang:1.23.10-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/excalidraw-server ./cmd/excalidraw-server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/excalidraw-server /usr/local/bin/excalidraw-server

USER nonroot:nonroot
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD ["/usr/local/bin/excalidraw-server", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/excalidraw-server"]
CMD ["serve"]
