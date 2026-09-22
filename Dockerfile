FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

RUN apk add --no-cache git
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/unraid-template-sync ./cmd/unraid-template-sync

FROM alpine:3.23

RUN apk add --no-cache ca-certificates git tini
COPY --from=build /out/unraid-template-sync /usr/local/bin/unraid-template-sync

EXPOSE 9000
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- http://127.0.0.1:9000/healthz >/dev/null || exit 1

ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/unraid-template-sync"]
