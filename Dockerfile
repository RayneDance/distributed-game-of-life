# -- Build stage --
FROM golang:1.24-alpine AS builder

# ca-certificates is needed at build time for go mod download (HTTPS).
# We also copy it into the runtime image so the app can verify TLS
# connections to Google APIs (Secret Manager, etc.).
RUN apk add --no-cache ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /golive .

# -- Runtime stage --
# scratch has zero filesystem content. We must explicitly copy anything
# the binary needs at runtime, including the CA certificate bundle.
FROM scratch

# Without this, all outbound TLS connections (Secret Manager, Redis TLS,
# Prometheus remote write, etc.) fail with x509: unknown authority.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /golive   /golive
COPY --from=builder /src/viewport /viewport

# Cloud Run injects PORT; default to 8080 so local docker run still works.
ENV PORT=8080

EXPOSE 8080

ENTRYPOINT ["/golive"]
