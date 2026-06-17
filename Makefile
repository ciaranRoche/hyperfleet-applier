.DEFAULT_GOAL := help

GO ?= go
BIN_DIR := bin
BINARY_NAME := $(BIN_DIR)/applier

GIT_SHA ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

GOFLAGS ?= -trimpath
LDFLAGS := -s -w

CONTAINER_TOOL ?= $(shell command -v podman 2>/dev/null || command -v docker 2>/dev/null)
PLATFORM ?= linux/amd64

IMAGE_REGISTRY ?= quay.io/openshift-hyperfleet
IMAGE_NAME ?= hyperfleet-applier

QUAY_USER ?=
DEV_TAG ?= dev-$(GIT_SHA)

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the applier binary for the container's target platform (linux/amd64)
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY_NAME) ./cmd/applier

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

.PHONY: test
test: ## Run unit tests
	$(GO) test -race ./...

.PHONY: check-container-tool
check-container-tool:
	@if [ -z "$(CONTAINER_TOOL)" ]; then echo "Error: podman or docker is required" >&2; exit 1; fi

.PHONY: image-dev
image-dev: check-container-tool build ## Build and push dev image to quay.io/$(QUAY_USER) (requires QUAY_USER)
ifndef QUAY_USER
	@echo "Error: QUAY_USER is not set"
	@echo "Usage: QUAY_USER=myuser make image-dev"
	@exit 1
endif
	@echo "Building dev image quay.io/$(QUAY_USER)/$(IMAGE_NAME):$(DEV_TAG) ..."
	$(CONTAINER_TOOL) build --platform $(PLATFORM) \
		-t quay.io/$(QUAY_USER)/$(IMAGE_NAME):$(DEV_TAG) .
	@echo "Pushing dev image..."
	$(CONTAINER_TOOL) push quay.io/$(QUAY_USER)/$(IMAGE_NAME):$(DEV_TAG)
	@echo ""
	@echo "Pushed: quay.io/$(QUAY_USER)/$(IMAGE_NAME):$(DEV_TAG)"
