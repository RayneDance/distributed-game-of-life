# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /golive .

# ── Runtime stage ─────────────────────────────────────────────────────────────
# scratch = zero-byte base image; no shell, no package manager, minimal attack surface.
FROM scratch

COPY --from=builder /golive  /golive
COPY --from=builder /src/viewport /viewport

# Cloud Run injects PORT; default to 8080 so local docker run still works.
ENV PORT=8080

EXPOSE 8080

ENTRYPOINT ["/golive"]
