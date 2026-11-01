VERSION  ?= dev
BIN      ?= waymark
IMAGE    ?= waymark:$(VERSION)
MAIN_PKG ?= ./cmd/waymark
LDFLAGS  := -s -w -X main.version=$(VERSION)

.PHONY: all build test vet fmt check image fixture fixtures demo

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) $(MAIN_PKG)

test:
	go test -race ./...

vet:
	go vet ./...

# Rewrites files in place and lists the ones it changed.
fmt:
	gofmt -l -w .

# gofmt -l exits 0 even when it names unformatted files, so the output itself
# is the failure signal.
check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	go vet ./...
	go test -race ./...

image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

# Serves testdata/site.json (made from the demo world) with no network
# access, for web work.
fixture:
	go run $(MAIN_PKG) -fixture testdata/site.json -listen 127.0.0.1:62585

# Rewrites every generated file under testdata/ from the demo world.
fixtures:
	go test ./internal/demo -run TestRegenerateFixtures -update

# The real pipeline against the built-in fake forge: an invented team's
# projects, no config and no network.
demo:
	go run $(MAIN_PKG) -demo -listen 127.0.0.1:62586
