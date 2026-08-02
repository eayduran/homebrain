.PHONY: fmt fmt-check vet test test-race test-lambda build docker-build compose-config verify

GO ?= go
NODE ?= node

fmt:
	$(GO)fmt -w cmd internal

fmt-check:
	test -z "$$($(GO)fmt -l cmd internal)"

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

test-lambda:
	$(NODE) --test lambda/index.test.mjs

build:
	$(GO) build ./cmd/rtc-server

docker-build:
	docker build .

compose-config:
	docker compose config

verify: fmt-check vet test test-race test-lambda docker-build compose-config
