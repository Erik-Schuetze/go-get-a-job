MODULE    := github.com/Erik-Schuetze/go-get-a-job
BINARY    := go-get-a-job
IMAGE     := go-get-a-job:latest
CONFIG    ?= config/config.example.yaml

# Tool versions are pinned here rather than in each workflow so that CI and
# a local `make lint` / `make vuln` are guaranteed to run the same thing.
GOLANGCI_LINT_VERSION := v2.13.2
GOVULNCHECK_VERSION   := v1.8.0
GOLANGCI_LINT         := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK           := go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

.PHONY: build test vet fmt lint vuln run docker-build clean

build:
	CGO_ENABLED=0 go build -o bin/$(BINARY) ./cmd/go-get-a-job

test:
	go test ./...

# -race is worth the slower run here: the pipeline is concurrent per source.
test-race:
	go test ./... -race

vet:
	go vet ./...

# Reports files that need formatting; does not rewrite them, so it is safe
# to run in CI.
fmt:
	gofmt -l -s .

lint:
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) fmt --diff

vuln:
	$(GOVULNCHECK) ./...

run: build
	./bin/$(BINARY) --config $(CONFIG)

docker-build:
	docker build -t $(IMAGE) .

clean:
	rm -rf bin/
