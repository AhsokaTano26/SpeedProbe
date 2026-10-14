.PHONY: build test vet check clean release release-check

VERSION ?= dev
DIST ?= dist

build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/speedprobe ./cmd/speedprobe

test:
	go test -race ./...

vet:
	go vet ./...

check: test vet
	@test -z "$$(gofmt -l cmd internal)"

clean:
	go clean

release:
	bash scripts/package-release.sh "$(VERSION)" "$(DIST)"

release-check:
	python3 scripts/test_release.py
