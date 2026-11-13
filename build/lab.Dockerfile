# The lab's own machinery: crowd, prober, agent, architect, narrator, twin, and seeder.
# Static Go binaries, a few MB each, so the VM images and the dev stack share one build.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
COPY contracts contracts
COPY deploy/k8s deploy/k8s
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/ ./cmd/...

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/ /usr/local/bin/
USER 65534
