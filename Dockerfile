# syntax=docker/dockerfile:1

# Builder: compile WASM bundle and server binary
FROM golang:1.24 AS builder
WORKDIR /src

# deps first for better cache
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build WASM and include wasm_exec.js (path varies by Go distro)
RUN GOOS=js GOARCH=wasm go build -o web/app.wasm ./cmd/web && \
    WASM_SRC="$(go env GOROOT)/misc/wasm/wasm_exec.js"; \
    if [ ! -f "$WASM_SRC" ]; then WASM_SRC="$(go env GOROOT)/lib/wasm/wasm_exec.js"; fi; \
    if [ ! -f "$WASM_SRC" ]; then echo "wasm_exec.js not found in GOROOT" >&2; exit 1; fi; \
    cp "$WASM_SRC" web/wasm_exec.js

# Build server (static binary)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/server ./cmd/serve
RUN mkdir -p /out/web && cp -r web/* /out/web/

# Runtime image
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/server /app/server
COPY --from=builder /out/web /app/web

EXPOSE 8080
ENTRYPOINT ["/app/server"]
CMD ["-web", "/app/web", "-listen", ":8080"]
