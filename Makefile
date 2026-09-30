.PHONY: all build server server-open \
        server-linux image image-push deploy deploy-manifest

all: build

# Container image, deployed via the sibling GitOps repo. Podman, never docker,
# matching the other apps deployed the same way (see e.g. ../photosee).
#
# jmelis/pajaros on quay.io defaults to private (unlike the older jmelis/*
# repos there) and nothing in the cluster holds an imagePullSecret, so the
# image lives in jmelis/birdquiz instead -- created public from the start.
IMAGE    ?= quay.io/jmelis/birdquiz
TAG      ?= $(shell git rev-parse --short HEAD)
DEPLOYER ?= ../docker-compose-deployer
MANIFEST ?= $(DEPLOYER)/k8s/applications/birdquiz/deployment.yaml

build:
	cd server && go build -o birdquiz-server .

# Requires EBIRD_API_KEY (see .envrc); direnv exports it automatically.
# Sign-in (needed only to save hotspots) is offered if GOOGLE_AUTH_ENABLED/APPLE_AUTH_ENABLED (plus their
# credentials) are set in the environment -- see server/main.go's env var docs.
# HOST is forced to 0.0.0.0 here because many shells (macOS in particular)
# already export HOST as the machine's hostname, which would otherwise
# silently override main.go's own "listen on every interface" default.
server:
	cd server && HOST=0.0.0.0 go run .

# Same as `server`, but forces the open/development mode (no login required)
# regardless of any auth env vars already set, e.g. in .envrc.
server-open:
	cd server && HOST=0.0.0.0 GOOGLE_AUTH_ENABLED= APPLE_AUTH_ENABLED= go run .

# Pure Go (modernc.org/sqlite has no cgo), so this cross-compiles from the Mac
# with no toolchain and lands straight in a distroless container.
server-linux:
	cd server && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -ldflags="-s -w -X main.version=$(TAG)" -o birdquiz-server-linux-amd64 .

image: server-linux
	podman build --platform linux/amd64 -t $(IMAGE):$(TAG) -t $(IMAGE):latest .

image-push: image
	podman push $(IMAGE):$(TAG)
	podman push $(IMAGE):latest

# Build, push, and point the deployer's manifest at what was just pushed.
# Checks run BEFORE the build: discovering the manifest is missing once the
# image is already in the registry leaves a tag nothing references.
deploy:
	@test -d "$(DEPLOYER)" || { \
	  echo 'no deployer repo at $(DEPLOYER) -- clone it beside this one, or pass DEPLOYER=<path>' >&2; \
	  exit 1; }
	@test -f "$(MANIFEST)" || { \
	  echo 'no manifest at $(MANIFEST) -- create k8s/applications/birdquiz/ there first' >&2; \
	  exit 1; }
	@# A tag is a claim about which commit is inside the image. Building from a
	@# dirty tree makes that claim false, and the image outlives the memory of
	@# having ignored this.
	@test -z "$$(git status --porcelain)" || { \
	  echo 'working tree is dirty -- $(TAG) would not describe what is in the image' >&2; \
	  echo 'commit first, or DIRTY=1 to override' >&2; \
	  $(if $(DIRTY),true,exit 1); }
	@$(MAKE) image-push
	@$(MAKE) deploy-manifest

# Rewrites only the tag on the line that pins THIS image, leaving any
# unrelated image reference in the same file alone.
deploy-manifest:
	@test -f "$(MANIFEST)" || { echo 'no manifest at $(MANIFEST)' >&2; exit 1; }
	@old=$$(grep -oE '$(IMAGE):[^"[:space:]]+' "$(MANIFEST)" | head -1); \
	 test -n "$$old" || { \
	   echo 'no $(IMAGE) line in $(MANIFEST) -- patch it by hand' >&2; exit 1; }; \
	 if [ "$$old" = "$(IMAGE):$(TAG)" ]; then \
	   echo "manifest already pins $(IMAGE):$(TAG)"; \
	 else \
	   sed -i '' "s|$$old|$(IMAGE):$(TAG)|" "$(MANIFEST)"; \
	   echo "manifest: $$old -> $(IMAGE):$(TAG)"; \
	 fi
	@echo
	@echo 'ArgoCD deploys what is COMMITTED there, not what is on disk:'
	@cd "$(DEPLOYER)" && git -c color.ui=always diff --stat -- k8s/applications/birdquiz/ | sed 's/^/  /'
	@echo "  cd $(DEPLOYER) && git commit -am 'birdquiz $(TAG)' && git push"
