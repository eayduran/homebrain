FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/rtc-server ./cmd/rtc-server

FROM alpine:3.23

RUN apk add --no-cache ca-certificates wget \
    && addgroup -S -g 10001 homebrain \
    && adduser -S -D -H -u 10001 -G homebrain homebrain \
    && mkdir -p /data/recordings \
    && chown -R 10001:10001 /data

COPY --from=build --chown=10001:10001 /out/rtc-server /usr/local/bin/rtc-server

USER 10001:10001
EXPOSE 8080/tcp
ENTRYPOINT ["/usr/local/bin/rtc-server"]
