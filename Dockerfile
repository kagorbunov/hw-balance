FROM golang:1.22.5 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -o /bin/hw-balance ./cmd/server

FROM gcr.io/distroless/static-debian12

COPY --from=builder /bin/hw-balance /hw-balance

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/hw-balance"]
