GOPATH ?= $(shell go env GOPATH)

.PHONY: all
all: format lint test

.PHONY: lint-go
lint-go: 
	./scripts/install-lint.sh
	${GOPATH}/bin/golangci-lint run

.PHONY: lint-shell
lint-shell:
	shellcheck --source-path=.:scripts scripts/*.sh

.PHONY: lint
lint: lint-go lint-shell

.PHONY: format-go
format-go:
	test -x $(GOPATH)/bin/goimports || go install golang.org/x/tools/cmd/goimports@latest
	$(GOPATH)/bin/goimports -local github.com/athanorlabs/go-p2p-net -w .

.PHONY: format-shell
format-shell:
	shfmt -w scripts/*.sh

.PHONY: format
format: format-go format-shell

.PHONY: test
test: 
	go test ./... -v -timeout=5m -count=1
