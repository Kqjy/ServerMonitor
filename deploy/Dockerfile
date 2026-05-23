FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund || npm install --no-audit --no-fund
COPY web/ ./
COPY internal/server/web/dist /internal/server/web/dist
RUN npm run build

FROM golang:1.26-alpine AS gobuild
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /internal/server/web/dist ./internal/server/web/dist
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server
RUN go build -trimpath -ldflags="-s -w" -o /out/agent ./cmd/agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=gobuild /out/server /usr/local/bin/server
COPY --from=gobuild /out/agent  /usr/local/bin/agent
USER nonroot
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=60s --retries=3 \
  CMD ["/usr/local/bin/server", "healthz"]
ENTRYPOINT ["/usr/local/bin/server"]
