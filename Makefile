GO ?= go
GOFMT ?= gofmt
BINARY ?= build/bargeboard
CONFIG ?= config.yaml

.PHONY: build check clean components fmt fmt-check mod-check run test validate vet

build:
	mkdir -p $(dir $(BINARY))
	$(GO) build -o $(BINARY) .

check: fmt-check mod-check test vet validate

clean:
	$(RM) -r build

components:
	$(GO) run . components

fmt:
	$(GO) fmt ./...

fmt-check:
	@files="$$(mktemp)" || exit $$?; \
	trap 'rm -f "$$files"' EXIT HUP INT TERM; \
	git ls-files --cached --others --exclude-standard -z -- '*.go' >"$$files" || exit $$?; \
	unformatted="$$(xargs -0 $(GOFMT) -l -- <"$$files")" || exit $$?; \
	if [ -n "$$unformatted" ]; then \
		printf 'These files need gofmt:\n%s\n' "$$unformatted"; \
		exit 1; \
	fi

mod-check:
	$(GO) mod tidy -diff
	$(GO) mod verify

run:
	$(GO) run . --config $(CONFIG)

test:
	$(GO) test -mod=readonly -count=1 -timeout=5m ./...

validate:
	$(GO) run -mod=readonly . validate --config $(CONFIG)

vet:
	$(GO) vet -mod=readonly ./...
