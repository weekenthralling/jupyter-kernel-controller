FROM golang:1.22 AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o kernel-controller

FROM ubuntu:22.04

WORKDIR /app/

COPY --from=builder /build/kernel-controller .

CMD ["./kernel-controller"]