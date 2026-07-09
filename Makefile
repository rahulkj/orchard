VERSION ?= dev
LDFLAGS := -X github.com/rahulkj/orchard/cmd.Version=$(VERSION)

.PHONY: build
build:
	go build -ldflags "$(LDFLAGS)" -o orchard .

.PHONY: vet
vet:
	go vet ./...

.PHONY: fmt
fmt:
	gofmt -l .

.PHONY: lint
lint:
	golangci-lint run ./...

.PHONY: test
test:
	go test ./...

.PHONY: check
check: fmt vet lint test

.PHONY: clean
clean:
	rm -f orchard
