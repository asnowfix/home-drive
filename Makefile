.PHONY: help build build-mac build-arm64 build-amd64 vet check test test-linux \
	test-pi deploy-pi dev-create dev-delete dev-setup dev-shell clean \
	install-systemd install-package install uninstall start stop restart \
	status logs

# `make` alone builds for the host.
.DEFAULT_GOAL := build

ME ?= $(shell whoami)

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

help:
	@echo "Available targets:"
	@echo "  build          - Build ./$(DIST)/$(BINARY) for the host (default)"
	@echo "  check          - go vet + the base test suite with the race detector"
	@echo "  install        - Build, then install the binary and systemd units and enable homedrive@$(ME) (uses sudo, Linux only)"
	@echo "  uninstall      - Disable and remove the installed service and binary (uses sudo)"
	@echo "  start|stop|restart - Control homedrive@$(ME).service (uses sudo)"
	@echo "  status|logs    - Show service status / follow its journal"
	@echo "  build-arm64|build-amd64|build-mac - Cross-compile into $(DIST)/"
	@echo "  test           - go test only (no vet, no race detector)"
	@echo "  test-linux     - Race-detector tests in the OrbStack VM"
	@echo "  deploy-pi      - build-arm64 + scp + restart on the NAS"
	@echo "  clean          - Remove $(DIST)/"

build:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY) $(CMD)

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

vet:
	go vet ./homedrive/...

# The base suite: vet plus every package under the race detector. On Linux
# this exercises real inotify, so it is the same suite CI runs.
check: vet
	go test -race ./homedrive/...

# macOS-native tests (compile check + platform-agnostic suite).
test:
	go test ./homedrive/...

# Linux tests with real inotify, run inside the OrbStack `dev` VM.
test-linux: dev-setup
	$(ORB_RUN) 'cd $(CURDIR)/homedrive && go test -race ./...'

test-pi:
	ssh fix@nas.local 'cd /tmp/homedrive-test && go test ./...'

# Installs to /usr/bin, matching install-package and the systemd unit's
# ExecStart=/usr/bin/homedrive run -- NOT /usr/local/bin. Diverging from
# that path would leave a stale binary shadowing the real one on PATH
# for anyone running `homedrive` interactively (see issue #68).
deploy-pi: build-arm64
	scp $(DIST)/$(BINARY)-arm64 fix@nas.local:/tmp/$(BINARY)
	ssh fix@nas.local 'sudo install /tmp/$(BINARY) /usr/bin/'
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

# Must be run as root (sudo make install-systemd / sudo make install-package).
install-systemd:
	install -d -m 0755 /etc/systemd/system
	install -m 0644 $(LINUX_DIR)/homedrive@.service /etc/systemd/system/
	install -d -m 0755 /etc/default
	install -m 0644 $(LINUX_DIR)/homedrive.default /etc/default/homedrive

install-package: build-arm64 install-systemd
	install -m 0755 $(DIST)/$(BINARY)-arm64 $(PREFIX)/bin/$(BINARY)
	install -m 0644 $(LINUX_DIR)/99-homedrive-inotify.conf /etc/sysctl.d/
	install -m 0644 $(LINUX_DIR)/homedrive.logrotate /etc/logrotate.d/homedrive
	install -d -m 0755 /etc/homedrive
	install -m 0644 $(LINUX_DIR)/config.yaml /etc/homedrive/config.yaml
	$(LINUX_DIR)/postinst.sh

# --- Local install: homedrive@$(ME).service on this machine -----------------
#
# `make install` builds for the host and installs with sudo (run it from your
# own terminal so sudo can prompt). It does not create the per-user config
# (~/.config/homedrive/config.yaml) or the rclone remote; the unit refuses to
# start until that config exists (ExecCondition).
#
# The unit ships ProtectHome=read-only, so when local_root (read from the
# per-user config) is under /home, a drop-in grants write access to it and to
# ~/.config/rclone, where rclone persists refreshed OAuth tokens.
UNIT_DIR     := /etc/systemd/system
SERVICE      := homedrive@$(ME).service
USER_CONFIG  := $(HOME)/.config/homedrive/config.yaml
LOCAL_ROOT   := $(shell sed -n 's/^local_root:[[:space:]]*//p' $(USER_CONFIG) 2>/dev/null | tr -d "\"'")

install: build
ifeq ($(shell uname -s),Linux)
	sudo install -m 0755 $(DIST)/$(BINARY) $(PREFIX)/bin/$(BINARY)
	sudo install -m 0644 $(LINUX_DIR)/homedrive@.service $(UNIT_DIR)/
	sudo install -d -m 0755 /etc/default
	sudo install -m 0644 $(LINUX_DIR)/homedrive.default /etc/default/homedrive
	sudo install -m 0644 $(LINUX_DIR)/99-homedrive-inotify.conf /etc/sysctl.d/
	sudo install -m 0644 $(LINUX_DIR)/homedrive.logrotate /etc/logrotate.d/homedrive
	@if [ -n "$(LOCAL_ROOT)" ]; then \
		sudo install -d -m 0755 $(UNIT_DIR)/$(SERVICE).d; \
		printf '[Service]\nReadWritePaths=%s %s/.config/rclone\n' "$(LOCAL_ROOT)" "$(HOME)" \
			| sudo tee $(UNIT_DIR)/$(SERVICE).d/rw-paths.conf >/dev/null; \
	else \
		echo "note: $(USER_CONFIG) not found; skipping the ReadWritePaths drop-in (re-run 'make install' after creating it)"; \
	fi
	sudo $(LINUX_DIR)/postinst.sh
	sudo systemctl enable $(SERVICE)
	@echo "installed; 'make start' once ~/.config/homedrive/config.yaml and the rclone remote exist"
else
	$(error make install is only supported on Linux)
endif

uninstall:
	-sudo systemctl disable --now $(SERVICE)
	sudo rm -rf $(UNIT_DIR)/homedrive@.service $(UNIT_DIR)/$(SERVICE).d $(PREFIX)/bin/$(BINARY)
	sudo systemctl daemon-reload

start stop restart:
	sudo systemctl $@ $(SERVICE)

status:
	systemctl status $(SERVICE) --no-pager

logs:
	journalctl -u $(SERVICE) -f

clean:
	rm -rf $(DIST)
