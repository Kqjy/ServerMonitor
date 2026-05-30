FROM golang:1.26-alpine AS build
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/sm-agent ./cmd/agent

FROM gcr.io/distroless/static-debian12:latest
COPY --from=build /out/sm-agent /usr/local/bin/sm-agent
ENTRYPOINT ["/usr/local/bin/sm-agent"]
