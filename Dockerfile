FROM node:22-alpine AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-build /src/web/dist ./web/dist
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/otd ./cmd/otd && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/metad ./cmd/metad

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=go-build /out/otd /out/metad /usr/local/bin/
WORKDIR /data
VOLUME ["/data"]
EXPOSE 80
ENTRYPOINT ["/usr/local/bin/otd"]
CMD ["serve", "-db", "/data/otd.db", "-meta-db", "/data/meta.db", "-addr", "0.0.0.0:80"]
