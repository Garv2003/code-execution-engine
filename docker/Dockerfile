FROM golang:1.24-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server

FROM alpine:3.20

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /build/server .
COPY --from=builder /build/languages.json .
COPY --from=builder /build/playground ./playground

EXPOSE 8080

ENTRYPOINT ["./server"]
