.PHONY: build test verify clean

build:
	go build ./cmd/contextbridge

test:
	go test ./...
	go vet ./...

verify: test
	bash ./scripts/test-install-headless.sh

clean:
	rm -rf dist release-artifacts contextbridge
