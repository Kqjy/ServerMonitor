FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/sm-agent ./cmd/agent

FROM --platform=$BUILDPLATFORM alpine:3.20 AS restic
ARG TARGETARCH
ARG RESTIC_VERSION=0.19.0
RUN apk add --no-cache curl bzip2 coreutils
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) sha=13176fe6d89d4357947a2cd107218ab2873a5f9d8e1ac2d4cd1c8e07e6839c21 ;; \
      arm64) sha=e522ce6bf748d753fee8093e8ec59359972cf5b6bc65fc7c7cf38ae952351d91 ;; \
      arm)   sha=2997e6ebd953a551abe33172876ce1a88aa1bb29a93425b167747ece7a38c850 ;; \
      386)   sha=0b58b04a7d2fffe290ed00ea841e97662af33296a2ba6abb52d4c62612b2e1e6 ;; \
      *) echo "unsupported TARGETARCH for restic: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/restic.bz2 "https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_${TARGETARCH}.bz2"; \
    echo "${sha}  /tmp/restic.bz2" | sha256sum -c -; \
    bunzip2 -c /tmp/restic.bz2 > /out-restic; \
    chmod 0755 /out-restic

FROM gcr.io/distroless/static-debian12:latest
COPY --from=build /out/sm-agent /usr/local/bin/sm-agent
COPY --from=restic /out-restic /usr/local/bin/sm-restic
ENTRYPOINT ["/usr/local/bin/sm-agent"]
