# Backend del CORTEX-9 Interview Coach: binario Go estático (sin CGO; SQLite es
# modernc, puro Go) sobre distroless nonroot. Escucha en :8080 con /metrics en
# el mismo puerto.
FROM golang:1.27-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=1.0.0
RUN go build -ldflags="-s -w -X main.version=${VERSION}" -o /out/coach ./cmd/coach \
    && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/coach /app/coach
COPY --from=build --chown=65532:65532 /out/data /data
ENV CX9_HTTP_ADDR=:8080 \
    CX9_DATA_DIR=/data \
    CX9_DATABASE_URL=sqlite:///data/cx9.db \
    CX9_LLM_PROVIDER=mock
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=4s --start-period=10s --retries=3 CMD ["/app/coach", "healthcheck"]
ENTRYPOINT ["/app/coach"]
CMD ["serve"]
