FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o main .

RUN apk --no-cache add git \
    && git clone https://github.com/yagi-agent/yagi-profiles.git ./yagi-profiles


# ---- runtime ----
FROM alpine:latest

RUN apk --no-cache add ca-certificates \
    && addgroup -g 65532 nonroot \
    && adduser -D -u 65532 -G nonroot nonroot

# Create writable directory and set ownership
RUN mkdir -p /data \
    && chown -R nonroot:nonroot /data

COPY --from=builder /app/main /main
COPY --from=builder /app/yagi-profiles /data/yagi-profiles

USER nonroot

VOLUME /data

ENTRYPOINT ["/main", "-data", "/data"]
