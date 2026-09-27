.PHONY: build test security verify clean

build:
	go build ./cmd/contextbridge

test:
	go test ./...
	go vet ./...

security:
	go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
	go run github.com/securego/gosec/v2/cmd/gosec@v2.29.0 -exclude=G103,G104,G204,G302,G304,G602 -exclude-generated ./...
	go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
	go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --config .gitleaks.toml --redact --no-banner .
	mkdir -p .tmp
	go build -trimpath -o .tmp/contextbridge ./cmd/contextbridge
	go run ./scripts/dast-smoke --binary .tmp/contextbridge

verify: test
	bash ./scripts/test-install-headless.sh

clean:
	rm -rf dist release-artifacts contextbridge
