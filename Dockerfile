FROM golang:1.25-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY src/ ./src/
RUN go build -o sort_by_date src/cmd/sort_by_date.go

FROM alpine:3.21

RUN apk add --no-cache \
    exiftool \
    rsync \
    openssh-client

WORKDIR /data

COPY --from=builder /build/sort_by_date /usr/local/bin/sort_by_date

ENTRYPOINT ["sort_by_date"]
