.PHONY: build test verify extensions clean

build:
	go build ./cmd/contextbridge

test:
	go test ./...
	go vet ./...

extensions:
	./scripts/package-extensions.sh

verify: test extensions
	./scripts/verify-extensions.sh
	git diff --exit-code -- extension/chromium extension/firefox

clean:
	rm -rf dist release-artifacts contextbridge
