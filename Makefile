# Image URL to use all building/pushing image targets
IMG ?= controller:latest

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

# Basic colors
BLACK=\033[0;30m
RED=\033[0;31m
GREEN=\033[0;32m
YELLOW=\033[0;33m
BLUE=\033[0;34m
PURPLE=\033[0;35m
CYAN=\033[0;36m
WHITE=\033[0;37m

# Text formatting
BOLD=\033[1m
UNDERLINE=\033[4m
RESET=\033[0m

# Get GITHUB_TOKEN from environment
GITHUB_TOKEN ?= $(shell echo $$GITHUB_TOKEN)

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: clean-build-files
clean-build-files: ## Clean up temporary build files
	@echo -e "Cleaning up temporary build files..."
	@rm -f $(PWD)/.github-token
	@rm -f Dockerfile.cross
	@echo -e "✅ Build files cleaned"

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test-setup
test-setup: manifests generate fmt vet setup-envtest ## Set up the environment for testing.

.PHONY: test
test: test-setup ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile coverage.out

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
.PHONY: test-e2e
test-e2e: manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind.
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@$(KIND) get clusters | grep -q 'kind' || { \
		echo "No Kind cluster is running. Please start a Kind cluster before running the e2e tests."; \
		exit 1; \
	}
	go test ./test/e2e/ -v -ginkgo.v

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	$(GOLANGCI_LINT) config verify

##@ Dependencies

deps: ## Download and verify dependencies
	@echo -e "Downloading dependencies..."
	@go mod download
	@go mod verify
	@go mod tidy
	@echo -e "Dependencies updated!"

update-deps: ## Update dependencies
	@echo -e "Updating dependencies..."
	@go get -u ./...
	@go mod tidy
	@echo -e "Dependencies updated!"

##@ Build

.PHONY: build-setup
build-setup: manifests generate fmt vet ## Set up environment for building.

.PHONY: build
build: ## Build manager binary.
	go build -o bin/kv-operator cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/

# NOTE: This project has dependencies on private repositories. Use docker-build-with-token or 
# docker-build-with-ssh for building with private repository access.
.PHONY: docker-build
docker-build: ## Build docker image with the manager (may fail with private dependencies).
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-build-with-github-token
docker-build-with-github-token: ## Build docker image with the manager using Docker secrets for GitHub token.
	@if [ -z "$$GITHUB_TOKEN" ]; then \
		echo "Error: GITHUB_TOKEN environment variable is required for private repository access"; \
		exit 1; \
	fi
	@echo -e "$$GITHUB_TOKEN" > $(PWD)/.github-token
	@chmod 600 $(PWD)/.github-token
	DOCKER_BUILDKIT=1 $(CONTAINER_TOOL) build --secret id=github_token,src=$(PWD)/.github-token -t ${IMG} -f Dockerfile.secrets .
	@rm -f $(PWD)/.github-token

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# - have SSH access to private repositories configured (for docker-buildx) OR GitHub token (for docker-buildx-with-token)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64#,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support (requires SSH access to private repos)
	# copy existing Dockerfile.buildx and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile.buildx
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile.buildx > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name kubevirt-operator-builder
	$(CONTAINER_TOOL) buildx use kubevirt-operator-builder
	- $(CONTAINER_TOOL) buildx build --ssh default --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm kubevirt-operator-builder
	rm Dockerfile.cross

.PHONY: docker-buildx-with-github-token
docker-buildx-with-github-token: ## Build and push docker image for the manager for cross-platform support using GitHub token
	@if [ -z "$$GITHUB_TOKEN" ]; then \
		echo "Error: GITHUB_TOKEN environment variable is required for private repository access"; \
		exit 1; \
	fi
	# copy existing Dockerfile.secrets and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile.secrets
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile.secrets > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name kubevirt-operator-builder
	$(CONTAINER_TOOL) buildx use kubevirt-operator-builder
	- echo "$$GITHUB_TOKEN" | $(CONTAINER_TOOL) buildx build --secret id=github_token,src=- --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm kubevirt-operator-builder
	rm Dockerfile.cross

.PHONY: test-build
test-build: ## Test that the Docker build works (tries SSH first, then secrets)
	@echo -e "Testing Docker build with private repository access..."
	@if ssh -T git@github.com 2>&1 | grep -q "successfully authenticated"; then \
		echo "Using SSH authentication..."; \
		$(MAKE) docker-build-with-ssh; \
	elif [ -n "$$GITHUB_TOKEN" ]; then \
		echo "Using secrets-based token authentication..."; \
		$(MAKE) docker-build-with-secrets; \
	else \
		echo "Error: Neither SSH nor GITHUB_TOKEN authentication is available."; \
		echo "Run 'make setup-build-env' for setup instructions."; \
		exit 1; \
	fi

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/kv-operator && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/kv-operator && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -



##@ Kubebuilder

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
HELM ?= helm
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GOSEC ?= $(LOCALBIN)/gosec

## Tool Versions
KUSTOMIZE_VERSION ?= v5.7.1
CONTROLLER_TOOLS_VERSION ?= v0.18.0
#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f "{{ .Version }}" k8s.io/api | awk -F'[v.]' '{printf "1.%d", $$3}')
GOLANGCI_LINT_VERSION ?= v2.4.0
GOSEC_VERSION ?= v2.22.8
# Dynamically derived versions (can be overridden)
KUBEVIRT_VERSION ?= $(shell curl -s https://storage.googleapis.com/kubevirt-prow/release/kubevirt/kubevirt/stable.txt)
CDI_VERSION ?= $(shell curl -s https://api.github.com/repos/kubevirt/containerized-data-importer/releases/latest | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

# Force reinstallation of golangci-lint with the currently active Go toolchain
.PHONY: golangci-lint-reinstall
golangci-lint-reinstall: ## Force reinstall golangci-lint (use if Go version changed)
	@echo "Reinstalling golangci-lint $(GOLANGCI_LINT_VERSION) with Go $$(go version | awk '{print $$3}')"
	rm -f $(GOLANGCI_LINT) $(GOLANGCI_LINT)-$(GOLANGCI_LINT_VERSION) || true
	$(MAKE) golangci-lint

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo -e "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: install-security-scanner
install-security-scanner: $(GOSEC) ## Install gosec security scanner locally (static analysis for security issues)
$(GOSEC): $(LOCALBIN)
	@set -e; echo "Attempting to install gosec $(GOSEC_VERSION)"; \
	if ! GOBIN=$(LOCALBIN) go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION) 2>/dev/null; then \
		echo "Primary install failed, attempting install from @main (compatibility fallback)"; \
		if ! GOBIN=$(LOCALBIN) go install github.com/securego/gosec/v2/cmd/gosec@main; then \
			echo "gosec installation failed for versions $(GOSEC_VERSION) and @main"; \
			exit 1; \
		fi; \
	fi; \
	echo "gosec installed at $(GOSEC)"; \
	chmod +x $(GOSEC)

##@ Security
.PHONY: go-security-scan
go-security-scan: install-security-scanner ## Run gosec security scan (fails on findings)
	$(GOSEC) ./...

.PHONY: go-security-scan-docker
go-security-scan-docker: ## Run gosec scan using official container (alternative if local install fails)
	@echo "Running gosec via Docker container..."; \
	$(CONTAINER_TOOL) run --rm -v $(PWD):/workspace -w /workspace securego/gosec/gosec:latest ./...

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef

##@ CRDs & Resources
.PHONY: k8s-install-viti-crds k8s-download-viti-crds k8s-uninstall-viti-crds

k8s-install-viti-crds: ## Install CRDs into a cluster
	@echo -e "${GREEN}Installing CRDs...${RESET}"
	@if [ ! -d "hack/crds" ]; then \
		echo -e "${RED}Error: hack/crds directory does not exist${RESET}"; \
		echo -e "${YELLOW}Run 'make k8s-download-viti-crds' first to download the CRDs (requires GITHUB_TOKEN)${RESET}"; \
		exit 1; \
	fi
	@if [ -z "$$(find hack/crds -name '*.yaml' -type f 2>/dev/null)" ]; then \
		echo -e "${RED}Error: No YAML files found in hack/crds directory${RESET}"; \
		echo -e "${YELLOW}Run 'make k8s-download-viti-crds' first to download the CRDs (requires GITHUB_TOKEN)${RESET}"; \
		exit 1; \
	fi
	@echo "Found CRD files:"
	@ls -1 hack/crds/*.yaml | sed 's/^/  - /'
	${KUBECTL} apply -f hack/crds/
	@echo -e "${GREEN}CRDs installed successfully${RESET}"

k8s-download-viti-crds: ## Download CRDs from private repository (requires GITHUB_TOKEN)
	@echo -e "${GREEN}Downloading CRDs from private repository...${RESET}"
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo -e "${RED}Error: GITHUB_TOKEN environment variable is required for private repository access${RESET}"; \
		echo -e "${YELLOW}Please set your GITHUB_TOKEN environment variable${RESET}"; \
		exit 1; \
	fi
	@mkdir -p hack/crds
	@echo "Downloading vitistack.io_vitistacks.yaml..."
	@if ! curl --fail -H "Authorization: token $(GITHUB_TOKEN)" \
		-H "Accept: application/vnd.github.v3.raw" \
		-o hack/crds/vitistack.io_vitistacks.yaml \
		https://api.github.com/repos/vitistack/crds/contents/crds/vitistack.io_vitistacks.yaml; then \
		echo -e "${RED}Error: Failed to download vitistack.io_vitistacks.yaml${RESET}"; \
		exit 1; \
	fi
	@echo "Downloading vitistack.io_kubernetesproviders.yaml..."
	@if ! curl --fail -H "Authorization: token $(GITHUB_TOKEN)" \
		-H "Accept: application/vnd.github.v3.raw" \
		-o hack/crds/vitistack.io_kubernetesproviders.yaml \
		https://api.github.com/repos/vitistack/crds/contents/crds/vitistack.io_kubernetesproviders.yaml; then \
		echo -e "${RED}Error: Failed to download vitistack.io_kubernetesproviders.yaml${RESET}"; \
		exit 1; \
	fi
	@echo "Downloading vitistack.io_machineproviders.yaml..."
	@if ! curl --fail -H "Authorization: token $(GITHUB_TOKEN)" \
		-H "Accept: application/vnd.github.v3.raw" \
		-o hack/crds/vitistack.io_machineproviders.yaml \
		https://api.github.com/repos/vitistack/crds/contents/crds/vitistack.io_machineproviders.yaml; then \
		echo -e "${RED}Error: Failed to download vitistack.io_machineproviders.yaml${RESET}"; \
		exit 1; \
	fi
	@echo "Downloading vitistack.io_machines.yaml..."
	@if ! curl --fail -H "Authorization: token $(GITHUB_TOKEN)" \
		-H "Accept: application/vnd.github.v3.raw" \
		-o hack/crds/vitistack.io_machines.yaml \
		https://api.github.com/repos/vitistack/crds/contents/crds/vitistack.io_machines.yaml; then \
		echo -e "${RED}Error: Failed to download vitistack.io_machines.yaml${RESET}"; \
		exit 1; \
	fi
	@echo "Downloading vitistack.io_kubernetesclusters.yaml..."
	@if ! curl --fail -H "Authorization: token $(GITHUB_TOKEN)" \
		-H "Accept: application/vnd.github.v3.raw" \
		-o hack/crds/vitistack.io_kubernetesclusters.yaml \
		https://api.github.com/repos/vitistack/crds/contents/crds/vitistack.io_kubernetesclusters.yaml; then \
		echo -e "${RED}Error: Failed to download vitistack.io_kubernetesclusters.yaml${RESET}"; \
		exit 1; \
	fi
	@echo -e "${GREEN}CRDs downloaded successfully${RESET}"

k8s-uninstall-viti-crds: check-kubectl ## Uninstall CRDs into a cluster
	@echo -e "${RED}Uninstalling CRDs...${RESET}"
	${KUBECTL} delete -f hack/crds/

##@ Kubernetes 

.PHONY: k8s-install-simple-storageclass
k8s-install-simple-storageclass: ## Install a default storage class for the cluster.
	@echo -e "Installing default storage class..."
	$(eval KUBERNETES_VERSION := $(shell kubectl version --output=json 2>/dev/null | jq -r '.serverVersion.major + "." + .serverVersion.minor' 2>/dev/null || kubectl version --output=yaml 2>/dev/null | grep gitVersion | head -1 | sed 's/.*v\([0-9]*\.[0-9]*\).*/\1/' || echo "1.31"))
	@if [ -z "$(KUBERNETES_VERSION)" ]; then \
		echo "Error: KUBERNETES_VERSION could not be determined"; \
		exit 1; \
	fi
	@echo -e "Detected Kubernetes version: $(KUBERNETES_VERSION)"
	@if [ "$$(echo "$(KUBERNETES_VERSION) >= 1.31" | bc -l)" -eq 1 ] 2>/dev/null || [ "$$(printf "$(KUBERNETES_VERSION)\n1.31" | sort -V | head -n1)" = "1.31" ]; then \
		$(KUBECTL) create namespace local-path-storage ; \
		$(KUBECTL) label namespace local-path-storage pod-security.kubernetes.io/enforce=privileged; \
		$(KUBECTL) apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/deploy/local-path-storage.yaml; \
		$(KUBECTL) patch storageclass local-path -p '{"metadata": {"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'; \
	else \
		echo "Kubernetes version $(KUBERNETES_VERSION) is below 1.31, no default storage class will be installed"; \
	fi
	@echo -e "✅ Default storage class installed."

.PHONY: k8s-uninstall-simple-storageclass
k8s-uninstall-simple-storageclass: ## Uninstall the default storage class from the cluster.
	@echo -e "Uninstalling default storage class..."
	$(KUBECTL) delete storageclass local-path --ignore-not-found=true
	$(KUBECTL) delete namespace local-path-storage --ignore-not-found=true
	@echo -e "✅ Default storage class uninstalled."

##@ KubeVirt Operator Installation
KUBEVIRTNAMESPACE := kubevirt
.PHONY: k8s-install-kubevirt
k8s-install-kubevirt: ## Install KubeVirt operator and CRDs into the K8s cluster specified in ~/.kube/config.
	@echo -e "Installing KubeVirt operator and CRDs..."
	@echo -e "Using KubeVirt version: $(KUBEVIRT_VERSION)"
	$(KUBECTL) create namespace kubevirt ; \
	$(KUBECTL) label namespace kubevirt pod-security.kubernetes.io/enforce=privileged; \
	$(KUBECTL) apply -f https://github.com/kubevirt/kubevirt/releases/download/$(KUBEVIRT_VERSION)/kubevirt-operator.yaml
	$(KUBECTL) apply -f https://github.com/kubevirt/kubevirt/releases/download/$(KUBEVIRT_VERSION)/kubevirt-cr.yaml
	
	@echo -e "🔄 Waiting for KubeVirt CR to become available..."
	@RESOURCE_NAME=$$(kubectl get kubevirt -n $(KUBEVIRTNAMESPACE) -o jsonpath="{.items[0].metadata.name}") && \
	kubectl wait kubevirt $$RESOURCE_NAME --for=condition=Available --timeout=5m -n $(KUBEVIRTNAMESPACE)

	@echo -e "🔄 Waiting for KubeVirt core components to be ready..."
	@for component in virt-operator virt-api virt-controller; do \
		echo "⏳ Waiting for deployment $$component..."; \
		kubectl rollout status deployment $$component -n $(KUBEVIRTNAMESPACE) || exit 1; \
	done

	@echo -e "🔄 Waiting for virt-handler pods to be ready..."
	@kubectl wait --for=condition=Ready pod -l kubevirt.io=virt-handler -n $(KUBEVIRTNAMESPACE) --timeout=300s

	@echo -e "✅ KubeVirt is fully deployed and ready."


.PHONY: k8s-uninstall-kubevirt
k8s-uninstall-kubevirt: ## Uninstall KubeVirt operator and CRDs from the K8s cluster specified in ~/.kube/config.
	@echo -e "Uninstalling KubeVirt operator and CRDs..."
	@echo -e "Using KubeVirt version: $(KUBEVIRT_VERSION)"
	@echo -e "Deleting KubeVirt CRDs..."
	$(KUBECTL) delete -f https://github.com/kubevirt/kubevirt/releases/download/$(KUBEVIRT_VERSION)/kubevirt-operator.yaml
	@echo -e "Deleting KubeVirt operator..."
	$(KUBECTL) delete -f https://github.com/kubevirt/kubevirt/releases/download/$(KUBEVIRT_VERSION)/kubevirt-cr.yaml
	@echo -e "Deleting KubeVirt namespace..."
	$(KUBECTL) delete namespace $(KUBEVIRTNAMESPACE)
	@echo -e "✅ KubeVirt operator and CRDs uninstalled."


.PHONY: k8s-kubevirt-emulation-patch
k8s-kubevirt-emulation-patch: ## Patch cluster to support KubeVirt.
	@echo -e "Patching Kind cluster to support KubeVirt..."
	$(KUBECTL) -n kubevirt patch kubevirt kubevirt --type=merge --patch '{"spec":{"configuration":{"developerConfiguration":{"useEmulation":true}}}}'
	@echo -e "✅ Kind cluster patched for KubeVirt."

.PHONY: k8s-install-kubevirt-containerized-data-importer
k8s-install-kubevirt-containerized-data-importer: ## Install KubeVirt CDI operator and CRDs into the K8s cluster specified in ~/.kube/config.
	@echo -e "Installing KubeVirt Containerized data importer operator and CRDs..."
	@echo -e "Using CDI version: $(CDI_VERSION)"
	$(KUBECTL) create -f https://github.com/kubevirt/containerized-data-importer/releases/download/$(CDI_VERSION)/cdi-operator.yaml
	$(KUBECTL) create -f https://github.com/kubevirt/containerized-data-importer/releases/download/$(CDI_VERSION)/cdi-cr.yaml

.PHONY: k8s-uninstall-kubevirt-containerized-data-importer
k8s-uninstall-kubevirt-containerized-data-importer: ## Uninstall KubeVirt CDI operator and CRDs from the cluster.
	@echo -e "Uninstalling KubeVirt Containerized data importer operator and CRDs..."
	@echo -e "Using CDI version: $(CDI_VERSION)"
	$(KUBECTL) delete -f https://github.com/kubevirt/containerized-data-importer/releases/download/$(CDI_VERSION)/cdi-operator.yaml
	$(KUBECTL) delete -f https://github.com/kubevirt/containerized-data-importer/releases/download/$(CDI_VERSION)/cdi-cr.yaml

.PHONY: versions
versions: ## Print key tool and dependency versions used by the Makefile
	@echo "KUSTOMIZE_VERSION=$(KUSTOMIZE_VERSION)"
	@echo "CONTROLLER_TOOLS_VERSION=$(CONTROLLER_TOOLS_VERSION)"
	@echo "ENVTEST_VERSION=$(ENVTEST_VERSION)"
	@echo "ENVTEST_K8S_VERSION=$(ENVTEST_K8S_VERSION)"
	@echo "GOLANGCI_LINT_VERSION=$(GOLANGCI_LINT_VERSION)"
	@echo "GOSEC_VERSION=$(GOSEC_VERSION)"
	@echo "KUBEVIRT_VERSION=$(KUBEVIRT_VERSION)"
	@echo "CDI_VERSION=$(CDI_VERSION)"
