FROM --platform=$BUILDPLATFORM node:24-alpine AS console
WORKDIR /src
COPY web/package.json web/package-lock.json ./web/
RUN npm --prefix web ci
COPY web ./web
RUN npm --prefix web run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG MOBILE_USE_VERSION=development
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=console /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w -X main.version=$MOBILE_USE_VERSION" -o /mobile-use ./cmd/mobile-use

FROM --platform=$BUILDPLATFORM alpine:3.22 AS runtime-files
RUN apk add --no-cache ca-certificates && addgroup -S mobileuse && adduser -S -G mobileuse mobileuse

FROM alpine:3.22
COPY --from=runtime-files /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=runtime-files /etc/passwd /etc/group /etc/
COPY --from=build /mobile-use /usr/local/bin/mobile-use
USER mobileuse
EXPOSE 8443 9443
ENTRYPOINT ["mobile-use"]
CMD ["serve", "--no-android"]
