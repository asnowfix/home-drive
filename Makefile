.PHONY: build-mac build-arm64 build-amd64 test test-linux test-pi deploy-pi \
	dev-create dev-delete dev-setup dev-shell clean install-systemd install-package

BINARY := homedrive
CMD    := ./homedrive/cmd/homedrive
DIST   := dist

# OrbStack Linux dev VM — used for tests that require real inotify.
# `dev-setup` is idempotent: creates the machine if missing, then installs
# Go and tools. Run once per workstation.
ORB_MACHINE := dev
ORB_DISTRO  := ubuntu:24.04
ORB_ARCH    := amd64
GO_VERSION  := 1.26.2

# Shell-mode invocation in the dev VM. `-s` runs through the login shell
# so /etc/profile.d/go.sh is sourced and `go` lands on PATH.
ORB_RUN := orb run -m $(ORB_MACHINE) -s

LDFLAGS := -s -w -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build-mac:
	@mkdir -p $(DIST)
	go build -o $(DIST)/$(BINARY) $(CMD)

build-arm64:
	@mkdir -p $(DIST)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" \
		-o $(DIST)/$(BINARY)-arm64 $(CMD)

build-amd64:
	@mkdir -p $(DIST)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" \
		-o $(DIST)/$(BINARY)-amd64 $(CMD)

# macOS-native tests (compile check + platform-agnostic suite).
test:
	go test ./homedrive/...

# Linux tests with real inotify, run inside the OrbStack `dev` VM.
test-linux: dev-setup
	$(ORB_RUN) 'cd $(CURDIR)/homedrive && go test -race ./...'

test-pi:
	ssh fix@nas.local 'cd /tmp/homedrive-test && go test ./...'

deploy-pi: build-arm64
	scp $(DIST)/$(BINARY)-arm64 fix@nas.local:/tmp/$(BINARY)
	ssh fix@nas.local 'sudo install /tmp/$(BINARY) /usr/local/bin/'
	ssh fix@nas.local 'sudo systemctl restart homedrive@fix.service'

# Provision the OrbStack `dev` machine if it does not exist.
dev-create:
	@if orb list 2>/dev/null | awk '{print $$1}' | grep -qx $(ORB_MACHINE); then \
		echo "orb machine '$(ORB_MACHINE)' already exists"; \
	else \
		echo "creating orb machine '$(ORB_MACHINE)' ($(ORB_DISTRO) $(ORB_ARCH))..."; \
		orb create -a $(ORB_ARCH) $(ORB_DISTRO) $(ORB_MACHINE); \
	fi

# Install Go and tooling in the dev VM. Idempotent.
dev-setup: dev-create
	@$(ORB_RUN) 'set -e; \
		if [ ! -x /usr/local/go/bin/go ] || [ "$$(/usr/local/go/bin/go env GOVERSION)" != "go$(GO_VERSION)" ]; then \
			echo "installing Go $(GO_VERSION)..."; \
			sudo apt-get update -qq && sudo apt-get install -y -qq curl ca-certificates git build-essential; \
			cd /tmp && curl -fsSL "https://go.dev/dl/go$(GO_VERSION).linux-$(ORB_ARCH).tar.gz" -o go.tar.gz; \
			sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go.tar.gz && rm -f go.tar.gz; \
			echo "export PATH=/usr/local/go/bin:\$$HOME/go/bin:\$$PATH" | sudo tee /etc/profile.d/go.sh > /dev/null; \
			sudo chmod +x /etc/profile.d/go.sh; \
		fi; \
		export PATH=/usr/local/go/bin:$$HOME/go/bin:$$PATH; \
		if [ ! -x $$HOME/go/bin/golangci-lint ]; then \
			echo "installing golangci-lint..."; \
			go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest; \
		fi; \
		go version; $$HOME/go/bin/golangci-lint --version'

dev-delete:
	orb delete $(ORB_MACHINE)

dev-shell: dev-setup
	$(ORB_RUN) 'cd $(CURDIR) && exec $$SHELL -l'

LINUX_DIR := homedrive/linux
PREFIX    := /usr

install-systemd:
	install -d -m 0755 /etc/systemd/system
	install -m 0644 $(LINUX_DIR)/homedrive@.service /etc/systemd/system/
	install -d -m 0755 /etc/default
	install -m 0644 $(LINUX_DIR)/homedrive.default /etc/default/homedrive
	systemctl daemon-reload

install-package: build-arm64 install-systemd
	install -m 0755 $(DIST)/$(BINARY)-arm64 $(PREFIX)/bin/$(BINARY)
	cd $(LINUX_DIR) && ./postinst.sh

clean:
	rm -rf $(DIST)
