FROM --platform=$BUILDPLATFORM tonistiigi/xx AS xx

# Build container
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder
COPY --from=xx / /
ARG TARGETPLATFORM

ENV CGO_ENABLED=1

RUN xx-apk add --no-cache ca-certificates git zlib-dev musl-dev gcc

WORKDIR /build

COPY go.mod go.sum ./

RUN xx-go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    xx-go build \
    -tags=jsoniter \
    -trimpath \
    -v \
    -o main cmd/worker/main.go

# verify with xx
RUN xx-verify /build/main

# Prod container
FROM alpine

RUN apk add --no-cache ca-certificates curl shadow

COPY --from=builder /build/locale /srv/worker/locale
COPY --from=builder /build/main /srv/worker/main

RUN useradd -m container
USER container

WORKDIR /srv/worker

ENTRYPOINT ["/srv/worker/main"]

#RUN apt-get update && apt-get upgrade -y && apt-get install -y ca-certificates curl
#
#COPY --from=builder /go/src/github.com/TicketsBot/worker/locale /srv/worker/locale
#COPY --from=builder /go/src/github.com/TicketsBot/worker/main /srv/worker/main
#
#RUN chmod +x /srv/worker/main
#
#RUN useradd -m container
#USER container
#WORKDIR /srv/worker
#
#CMD ["/srv/worker/main"]