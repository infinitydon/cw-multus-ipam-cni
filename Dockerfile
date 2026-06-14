FROM --platform=$BUILDPLATFORM golang:1.24 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/cw-multinet ./cmd/cw-multinet

FROM alpine:3.22
COPY --from=build /out/cw-multinet /cw-multinet
ENTRYPOINT ["/cw-multinet"]
