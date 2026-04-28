GO_TAGS := goolm
GO_PACKAGES := ./...
GO_FILES := $(shell find . -name '*.go' -type f)

.PHONY: test test-race build vet fmt

test:
	go test -tags $(GO_TAGS) $(TESTFLAGS) $(GO_PACKAGES)

test-race:
	go test -race -tags $(GO_TAGS) $(TESTFLAGS) $(GO_PACKAGES)

build:
	go build -tags $(GO_TAGS) $(GO_PACKAGES)

vet:
	go vet -tags $(GO_TAGS) $(GO_PACKAGES)

fmt:
	gofmt -w $(GO_FILES)
