FROM node:24-alpine AS console
WORKDIR /src
COPY web/package.json web/package-lock.json ./web/
RUN npm --prefix web ci
COPY web ./web
RUN npm --prefix web run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=console /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /mobile-use ./cmd/mobile-use

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -S mobileuse && adduser -S -G mobileuse mobileuse
COPY --from=build /mobile-use /usr/local/bin/mobile-use
USER mobileuse
EXPOSE 8443 9443
ENTRYPOINT ["mobile-use"]
CMD ["serve", "--no-android"]
