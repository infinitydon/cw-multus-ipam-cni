FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/cw-multinet ./cmd/cw-multinet
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/cw-multinet-agent ./cmd/cw-multinet-agent

FROM alpine:3.22
COPY --from=build /out/cw-multinet /cw-multinet
COPY --from=build /out/cw-multinet-agent /cw-multinet-agent
ENTRYPOINT ["/cw-multinet"]
