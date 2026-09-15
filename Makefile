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
	bash ./scripts/test-install-headless.sh
	@if command -v pwsh >/dev/null 2>&1; then pwsh -NoProfile -File ./scripts/test-build-release.ps1; else echo "Skipping release-script test: pwsh is unavailable"; fi
	git diff --exit-code -- extension/chromium extension/firefox

clean:
	rm -rf dist release-artifacts contextbridge
