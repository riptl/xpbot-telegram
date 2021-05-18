FROM golang:1.16-alpine AS builder
WORKDIR /app
COPY go.mod go.sum /app/
RUN go mod download
COPY *.go /app/
RUN CGO_ENABLED=0 go build -o xpbot-telegram .

FROM alpine
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/xpbot-telegram /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/xpbot-telegram"]
