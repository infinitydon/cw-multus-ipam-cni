IMAGE ?= ghcr.io/replace-me/cw-multus-ipam-cni:latest

.PHONY: build
build:
	go build -o bin/cw-multinet ./cmd/cw-multinet

.PHONY: test
test:
	go test ./...

.PHONY: image
image:
	docker build -t $(IMAGE) .

.PHONY: fmt
fmt:
	gofmt -w ./cmd
