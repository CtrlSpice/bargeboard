GO ?= go
BINARY ?= build/bargeboard
CONFIG ?= config.yaml

.PHONY: build check clean components fmt run test validate vet

build:
	mkdir -p $(dir $(BINARY))
	$(GO) build -o $(BINARY) .

check: test vet validate

clean:
	$(RM) -r build

components:
	$(GO) run . components

fmt:
	$(GO) fmt ./...

run:
	$(GO) run . --config $(CONFIG)

test:
	$(GO) test ./...

validate:
	$(GO) run . validate --config $(CONFIG)

vet:
	$(GO) vet ./...
