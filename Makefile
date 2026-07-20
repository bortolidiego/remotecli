.PHONY: all web-build go-build install test go-test race web-test swift-build swift-test fmt clean

all: web-build go-build

web-build:
	cd apps/web && npm run build

go-build:
	go build ./cmd/relay

install: go-build
	mkdir -p $(HOME)/.local/bin
	cp relay $(HOME)/.local/bin/relay

test: go-test web-test swift-test

go-test:
	go test ./...

race:
	go test -race ./...

web-test:
	cd apps/web && npm test

swift-build:
	cd helper/RelayHelper && swift build

swift-test:
	cd helper/RelayHelper && swift test

fmt:
	gofmt -w cmd internal shared

clean:
	rm -rf apps/web/dist relay helper/RelayHelper/.build
