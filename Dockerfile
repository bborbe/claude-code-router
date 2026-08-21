# Canonical Go Dockerfile — see ~/Documents/workspaces/coding/docs/go-dockerfile-guide.md
ARG DOCKER_REGISTRY=docker.io
FROM ${DOCKER_REGISTRY}/golang:1.27.0 AS build
ARG BUILD_GIT_VERSION=dev
ARG BUILD_GIT_COMMIT=none
ARG BUILD_DATE=unknown
COPY . /workspace
WORKDIR /workspace
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -mod=vendor -ldflags "-s -X github.com/bborbe/claude-code-router/pkg.version=${BUILD_GIT_VERSION}" -a -installsuffix cgo -o /main

FROM ${DOCKER_REGISTRY}/alpine:3.24 AS alpine
RUN apk --no-cache add ca-certificates

FROM scratch
ARG BUILD_GIT_VERSION=dev
ARG BUILD_GIT_COMMIT=none
ARG BUILD_DATE=unknown
COPY --from=build /main /main
COPY --from=alpine /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /
ENV ZONEINFO=/zoneinfo.zip
ENV BUILD_GIT_VERSION=${BUILD_GIT_VERSION}
ENV BUILD_GIT_COMMIT=${BUILD_GIT_COMMIT}
ENV BUILD_DATE=${BUILD_DATE}
ENTRYPOINT ["/main"]
