.PHONY: fmt vet test lint check install

fmt:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; echo "run: gofmt -w ."; exit 1; fi

vet:
	go vet ./...

test:
	go test ./...

lint:
	golangci-lint run

check: fmt vet test lint

install:
	go install ./cmd/director
