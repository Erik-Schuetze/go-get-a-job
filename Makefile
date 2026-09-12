MODULE  := github.com/Erik-Schuetze/go-get-a-job
BINARY  := go-get-a-job
IMAGE   := go-get-a-job:latest
CONFIG  ?= config/config.example.yaml

.PHONY: build test vet fmt lint run docker-build clean

build:
	CGO_ENABLED=0 go build -o bin/$(BINARY) ./cmd/go-get-a-job

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -s .

run: build
	./bin/$(BINARY) --config $(CONFIG)

docker-build:
	docker build -t $(IMAGE) .

clean:
	rm -rf bin/
