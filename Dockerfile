# Builds the Linux server and the Windows client in one go.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go test ./... \
 && go build -trimpath -ldflags="-s -w" -o /out/livecal-server ./cmd/server \
 && GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -H windowsgui" -o /out/livecal.exe ./cmd/client

FROM alpine:3.22
RUN apk add --no-cache tzdata ca-certificates wget \
 && adduser -D -H -u 10001 livecal \
 && mkdir -p /data && chown livecal /data
COPY --from=build /out/livecal-server /app/livecal-server
COPY --from=build /out/livecal.exe /app/livecal.exe
ENV DATA_DIR=/data CLIENT_EXE=/app/livecal.exe LISTEN_ADDR=:8933
USER livecal
VOLUME /data
EXPOSE 8933
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8933/healthz || exit 1
ENTRYPOINT ["/app/livecal-server"]
