FROM golang:1.25-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY src/ ./src/
RUN go build -o sort_by_date ./src/cmd/

FROM alpine:3.21

RUN apk add --no-cache \
    exiftool \
    rclone \
    openssh-client \
    --repository https://dl-cdn.alpinelinux.org/alpine/edge/community

WORKDIR /data

COPY --from=builder /build/sort_by_date /usr/local/bin/sort_by_date

ENTRYPOINT ["sort_by_date"]
