FROM golang:1.25.0 AS builder

WORKDIR /src
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY cmd ./cmd
COPY internal ./internal

ENV CGO_ENABLED=0
RUN go build -mod=vendor -trimpath -o /out/pii-masker ./cmd/pii-masker

FROM alpine:3.21

WORKDIR /app
RUN adduser -D -u 10001 appuser
COPY --from=builder /out/pii-masker /app/pii-masker
RUN mkdir -p /app/data && chown -R appuser:appuser /app

USER appuser
EXPOSE 8080

ENV PII_MASKER_ADDR=:8080 \
    PII_MASKER_STORAGE_DIR=/app/data \
    PII_MASKER_ENABLE_EMBEDDED_UPSTAGE_MOCK=true \
    PII_MASKER_UPSTAGE_BASE_URL=http://127.0.0.1:8080/internal/mock/upstage/inference \
    PII_MASKER_DEFAULT_MODEL=pii \
    PII_MASKER_DEFAULT_LANG=ko \
    PII_MASKER_DEFAULT_SCHEMA=oac

ENTRYPOINT ["/app/pii-masker"]
