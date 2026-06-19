BINARY ?= rke2-patcher
EXEC_MODE ?= binary
IMAGE_BUNDLES_DIR ?= $(CURDIR)/tests/docker/airgap/bundles
REPO ?= rancher
TAG ?= ${GITHUB_ACTION_TAG}
IMAGE = $(REPO)/rke2-patcher:$(TAG)
VERSION ?= dev
LDFLAGS ?= -X github.com/rancher/rke2-patcher/internal/version.Version=$(VERSION)

ifeq ($(TAG),)
TAG := v1.2.2$(BUILD_META)
endif

ifneq ($(filter v%,$(TAG)),)
VERSION := $(patsubst v%,%,$(TAG))
endif

.PHONY: help build build-image push-image \
	test-docker-image_cve test-docker-image_list test-docker-patch_components \
	test-docker-flannel_traefik_patch_components test-docker-patch_reconcile_component_ha \
	test-docker-reconcile test-docker-image_cve_local test-docker-merging_values \
	test-docker-reconcile_upgrade test-docker-airgap test-docker-multi_patcher_reconcile

help:
	@echo "Build:"
	@echo "  make build"
	@echo "  make build-image"
	@echo ""
	@echo "Docker scenario tests:"
	@echo "  make test-docker-image_cve EXEC_MODE=binary|pod"
	@echo "  make test-docker-image_list EXEC_MODE=binary|pod"
	@echo "  make test-docker-patch_components EXEC_MODE=binary|pod"
	@echo "  make test-docker-flannel_traefik_patch_components EXEC_MODE=binary|pod"
	@echo "  make test-docker-reconcile EXEC_MODE=binary|pod"
	@echo "  make test-docker-image_cve_local EXEC_MODE=binary"
	@echo "  make test-docker-merging_values EXEC_MODE=binary|pod"
	@echo "  make test-docker-reconcile_upgrade EXEC_MODE=binary|pod"
	@echo "  make test-docker-multi_patcher_reconcile EXEC_MODE=binary|pod"
	@echo "  make test-docker-patch_reconcile_component_ha EXEC_MODE=binary|pod"
	@echo "  make test-docker-airgap EXEC_MODE=binary IMAGE_BUNDLES_DIR=/path/to/bundles"
	@echo ""
	@echo "Defaults:"
	@echo "  EXEC_MODE=$(EXEC_MODE)"
	@echo "  IMAGE_BUNDLES_DIR=$(IMAGE_BUNDLES_DIR)"

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test-docker-image_cve: build
	EXEC_MODE=$(EXEC_MODE) go test -v -timeout=80m ./tests/docker/image_cve/image_cve_test.go -ginkgo.v -rke2Version v1.35.3+rke2r3 -patcherBin $(CURDIR)/$(BINARY)

test-docker-image_list: build
	EXEC_MODE=$(EXEC_MODE) go test -v -timeout=80m ./tests/docker/image_list/image_list_test.go -ginkgo.v -rke2Version v1.35.2+rke2r1 -patcherBin $(CURDIR)/$(BINARY)

test-docker-patch_components: build
	EXEC_MODE=$(EXEC_MODE) go test -v -timeout=80m ./tests/docker/patch_components/patch_components_test.go -ginkgo.v -rke2Version v1.35.3+rke2r3 -patcherBin $(CURDIR)/$(BINARY)

test-docker-flannel_traefik_patch_components: build
	EXEC_MODE=$(EXEC_MODE) go test -v -timeout=80m ./tests/docker/flannel_traefik_patch_components/flannel_traefik_patch_components_test.go -ginkgo.v -rke2Version v1.35.3+rke2r3 -patcherBin $(CURDIR)/$(BINARY)

test-docker-patch_reconcile_component_ha: build
	EXEC_MODE=$(EXEC_MODE) go test -v -timeout=120m ./tests/docker/patch_reconcile_component_ha/patch_reconcile_component_ha_test.go -ginkgo.v -rke2Version v1.35.3+rke2r3 -patcherBin $(CURDIR)/$(BINARY)

test-docker-reconcile: build
	EXEC_MODE=$(EXEC_MODE) go test -v -timeout=80m ./tests/docker/reconcile/reconcile_test.go -ginkgo.v -rke2Version v1.35.3+rke2r3 -patcherBin $(CURDIR)/$(BINARY)

test-docker-image_cve_local: build
	EXEC_MODE=$(EXEC_MODE) go test -v -timeout=80m ./tests/docker/image_cve_local/image_cve_local_test.go -ginkgo.v -rke2Version v1.35.3+rke2r3 -patcherBin $(CURDIR)/$(BINARY)

test-docker-merging_values: build
	EXEC_MODE=$(EXEC_MODE) go test -v -timeout=80m ./tests/docker/merging_values/merging_values_test.go -ginkgo.v -rke2Version v1.35.3+rke2r3 -patcherBin $(CURDIR)/$(BINARY)

test-docker-reconcile_upgrade: build
	EXEC_MODE=$(EXEC_MODE) go test -v -timeout=80m ./tests/docker/reconcile_upgrade/reconcile_upgrade_test.go -ginkgo.v -rke2Version v1.35.3+rke2r3 -patcherBin $(CURDIR)/$(BINARY)

test-docker-airgap: build
	EXEC_MODE=$(EXEC_MODE) go test -v -timeout=90m ./tests/docker/airgap/airgap_test.go -ginkgo.v -rke2Version v1.35.3+rke2r3 -patcherBin $(CURDIR)/$(BINARY) -imageBundlesDir $(IMAGE_BUNDLES_DIR)

test-docker-multi_patcher_reconcile: build
	EXEC_MODE=$(EXEC_MODE) go test -v -timeout=80m ./tests/docker/multi_patcher_reconcile/multi_patcher_reconcile_test.go -ginkgo.v -rke2Version v1.35.3+rke2r3 -patcherBin $(CURDIR)/$(BINARY)

build-image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

push-image:
	docker buildx build \
	$(IID_FILE_FLAG) \
	$(BUILDX_ARGS) \
	--build-arg VERSION=$(VERSION) \
	--sbom=true \
	--attest type=provenance,mode=max \
	--tag $(IMAGE) \
        --push \
        .
