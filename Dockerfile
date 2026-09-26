FROM golang:1.26.4-alpine3.23 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH:-amd64}" go build -trimpath -ldflags="-s -w" -o /out/friend-link-monitor ./cmd/friend-link-monitor

FROM alpine:3.23

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 monitor \
    && adduser -S -D -H -u 10001 -G monitor monitor \
    && mkdir -p /data \
    && chown monitor:monitor /data
COPY --from=build /out/friend-link-monitor /usr/local/bin/friend-link-monitor

USER monitor
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/friend-link-monitor"]
