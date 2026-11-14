.PHONY: help build build-mac build-arm64 build-amd64 vet check test test-linux \
	test-pi deploy-pi pi-host-guard dev-create dev-delete dev-setup dev-shell clean \
	install-systemd install-package install uninstall start stop restart \
	status logs install-user install-user-binary start-user stop-user restart-user \
	status-user logs-user uninstall-user deploy-local user-guard user-notices

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
	@echo "  install-user   - Build, then install binary + unit for 'systemctl --user' and enable it (NO sudo; Linux only)"
	@echo "  deploy-local   - Build, atomically replace ~/.local/bin/homedrive, restart the user unit (NO sudo)"
	@echo "  start-user|stop-user|restart-user|status-user|logs-user|uninstall-user - Control the user unit (NO sudo)"
	@echo "  build-arm64|build-amd64|build-mac - Cross-compile into $(DIST)/"
	@echo "  test           - go test only (no vet, no race detector)"
	@echo "  test-linux     - Race-detector tests in the OrbStack VM"
	@echo "  deploy-pi      - build-arm64, then install + restart the user unit on PI_HOST over ssh (NO sudo; PI_HOST required)"
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

# Remote targets (test-pi, deploy-pi). The target host has no default: this
# repo is public, so no hostname or IP lives here. Set PI_HOST on the command
# line or in the environment: `make deploy-pi PI_HOST=<nas-host>`.
PI_HOST     ?=
PI_USER     ?= $(ME)
# Extra options for both ssh and scp, e.g. `-i ~/.ssh/nas -o Port=2222` (use
# -o Port=, not -p/-P, which differ between ssh and scp). Empty by default.
PI_SSH_OPTS ?=
PI_TARGET   = $(PI_USER)@$(PI_HOST)

pi-host-guard:
	@if [ -z "$(PI_HOST)" ]; then \
		echo "PI_HOST is not set: there is no default target host." >&2; \
		echo "Usage: make <target> PI_HOST=<nas-host> [PI_USER=<user>] [PI_SSH_OPTS=...]" >&2; \
		exit 1; \
	fi

# NOTE (#81): this cannot work -- the NAS has no Go toolchain, and building on
# the NAS is against the release rules. Kept, pointed at PI_HOST, pending a
# maintainer decision to keep or remove it.
test-pi: pi-host-guard
	ssh $(PI_SSH_OPTS) $(PI_TARGET) 'cd /tmp/homedrive-test && go test ./...'

# deploy-pi deploys the *per-user* unit (see the user-mode section below) to
# $(PI_TARGET), with no sudo anywhere. The remote has no repo, make or Go, so
# every remote step is a plain ssh/scp command. The scripts below are passed
# as single-quoted ssh arguments, so they must not contain single quotes.
# (Recursive `=` because USER_UNIT/LINUX_DIR are defined further down.)
#
# Preflight, before anything is copied: refuse while the system unit is
# active/enabled (two agents must not share a local_root; mirrors user-guard)
# and while the user manager is unreachable. Non-PAM ssh sessions do not set
# XDG_RUNTIME_DIR, so it is exported here.
PI_SYSTEM_SERVICE = homedrive@$(PI_USER).service
PI_PREFLIGHT = \
	if systemctl is-active --quiet $(PI_SYSTEM_SERVICE) 2>/dev/null \
	   || systemctl is-enabled --quiet $(PI_SYSTEM_SERVICE) 2>/dev/null; then \
		echo "refusing: $(PI_SYSTEM_SERVICE) is active or enabled; two agents must not share a local_root." >&2; \
		echo "As root, on a console, run:" >&2; \
		echo "  systemctl disable --now $(PI_SYSTEM_SERVICE)" >&2; \
		exit 1; \
	fi; \
	export XDG_RUNTIME_DIR=/run/user/$$(id -u); \
	if ! systemctl --user show-environment >/dev/null 2>&1; then \
		echo "refusing: cannot reach the systemd user manager for $(PI_USER) (XDG_RUNTIME_DIR=$$XDG_RUNTIME_DIR)." >&2; \
		if [ "$$(loginctl show-user $(PI_USER) -p Linger --value 2>/dev/null)" != "yes" ]; then \
			echo "linger is off, so no user manager runs without a login session. As root, on a console, run:" >&2; \
			echo "  loginctl enable-linger $(PI_USER)" >&2; \
		fi; \
		exit 1; \
	fi; \
	mkdir -p "$$HOME/.local/bin" "$$HOME/.config/systemd/user"

# Install: both files were copied under temp names; rename(2) them into place
# (atomic, as install-user-binary does), then reload, enable and restart.
PI_INSTALL = \
	set -e; \
	export XDG_RUNTIME_DIR=/run/user/$$(id -u); \
	cd; \
	chmod 0755 .local/bin/.$(BINARY).new; \
	chmod 0644 .config/systemd/user/.$(USER_UNIT).new; \
	mv -f .local/bin/.$(BINARY).new .local/bin/$(BINARY); \
	mv -f .config/systemd/user/.$(USER_UNIT).new .config/systemd/user/$(USER_UNIT); \
	systemctl --user daemon-reload; \
	systemctl --user enable $(USER_UNIT); \
	systemctl --user restart $(USER_UNIT); \
	systemctl --user is-active $(USER_UNIT)

deploy-pi: pi-host-guard build-arm64
	ssh $(PI_SSH_OPTS) $(PI_TARGET) '$(PI_PREFLIGHT)'
	scp $(PI_SSH_OPTS) $(DIST)/$(BINARY)-arm64 $(PI_TARGET):.local/bin/.$(BINARY).new
	scp $(PI_SSH_OPTS) $(LINUX_DIR)/$(USER_UNIT) $(PI_TARGET):.config/systemd/user/.$(USER_UNIT).new
	ssh $(PI_SSH_OPTS) $(PI_TARGET) '$(PI_INSTALL)'

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

# --- User-mode install: `systemctl --user`, no sudo anywhere (#124) ----------
#
# Additive alternative to the system path above, for driving the daily
# build/install/restart loop from a session that has no tty (so no sudo
# prompt). Nothing here touches /etc, /usr or the system unit. The recipes
# never invoke privilege escalation; the few inherently-root, one-time steps
# (linger, disabling the system unit, inotify sysctl) are detected and the
# exact command is printed for a human to run on a console.
#
# Test overrides: USER_HOME (default $HOME), ME, SYSTEMCTL_USER (e.g. `echo`).
USER_HOME        ?= $(HOME)
USER_BIN_DIR     := $(USER_HOME)/.local/bin
USER_UNIT_DIR    := $(USER_HOME)/.config/systemd/user
USER_UNIT        := homedrive.service
SYSTEMCTL_USER   ?= systemctl --user
# Sessions without PAM/login (ssh without pam_systemd, cron, remote runners)
# leave this unset, which makes every `systemctl --user` fail; default it.
export XDG_RUNTIME_DIR ?= /run/user/$(shell id -u)
SYSTEM_SERVICE   :=homedrive@$(ME).service
USER_CONFIG_PATH := $(USER_HOME)/.config/homedrive/config.yaml

# Refuse to proceed while the system unit is active or enabled: two agents on
# one local_root would race each other and corrupt each other's journal view.
# Also refuse if the user manager is unreachable (no linger/login session).
user-guard:
	@if systemctl is-active --quiet $(SYSTEM_SERVICE) 2>/dev/null \
	   || systemctl is-enabled --quiet $(SYSTEM_SERVICE) 2>/dev/null; then \
		echo "refusing: $(SYSTEM_SERVICE) is active or enabled; two agents must not share a local_root." >&2; \
		echo "As root, on a console, run:" >&2; \
		echo "  systemctl disable --now $(SYSTEM_SERVICE)" >&2; \
		exit 1; \
	fi
	@if ! $(SYSTEMCTL_USER) show-environment >/dev/null 2>&1; then \
		echo "refusing: cannot reach the systemd user manager for $(ME)." >&2; \
		echo "(XDG_RUNTIME_DIR=$$XDG_RUNTIME_DIR)" >&2; \
		if [ "$$(loginctl show-user $(ME) -p Linger --value 2>/dev/null)" != "yes" ]; then \
			echo "linger is off for $(ME), so no user manager runs without a login session." >&2; \
			echo "As root, on a console, run:" >&2; \
			echo "  loginctl enable-linger $(ME)" >&2; \
		fi; \
		exit 1; \
	fi

# Non-fatal reminders of the inherently-root one-time steps. Never fails.
user-notices:
	@if [ "$$(loginctl show-user $(ME) -p Linger --value 2>/dev/null)" != "yes" ]; then \
		echo "notice: linger is off for $(ME); the user unit will only run while logged in." >&2; \
		echo "As root, on a console, run:" >&2; \
		echo "  loginctl enable-linger $(ME)" >&2; \
	fi
	@want_w=$$(sed -n 's/^fs.inotify.max_user_watches=//p' $(LINUX_DIR)/99-homedrive-inotify.conf); \
	want_i=$$(sed -n 's/^fs.inotify.max_user_instances=//p' $(LINUX_DIR)/99-homedrive-inotify.conf); \
	have_w=$$(cat /proc/sys/fs/inotify/max_user_watches 2>/dev/null || echo 0); \
	have_i=$$(cat /proc/sys/fs/inotify/max_user_instances 2>/dev/null || echo 0); \
	if [ "$$have_w" -lt "$$want_w" ] || [ "$$have_i" -lt "$$want_i" ]; then \
		echo "notice: inotify limits are low (watches $$have_w < $$want_w or instances $$have_i < $$want_i)." >&2; \
		echo "As root, on a console, run:" >&2; \
		echo "  install -m 0644 $(CURDIR)/$(LINUX_DIR)/99-homedrive-inotify.conf /etc/sysctl.d/ && sysctl --system" >&2; \
	fi
	@if [ ! -f "$(USER_CONFIG_PATH)" ]; then \
		echo "notice: $(USER_CONFIG_PATH) not found; the unit will not start until it exists." >&2; \
	fi

# Install to a temp name in the same directory, then rename(2): a running
# process (or a concurrent exec) never sees a half-written binary.
install-user-binary: build
	install -d -m 0755 $(USER_BIN_DIR)
	install -m 0755 $(DIST)/$(BINARY) $(USER_BIN_DIR)/.$(BINARY).new
	mv -f $(USER_BIN_DIR)/.$(BINARY).new $(USER_BIN_DIR)/$(BINARY)

install-user: user-guard install-user-binary user-notices
ifeq ($(shell uname -s),Linux)
	install -d -m 0755 $(USER_UNIT_DIR)
	install -m 0644 $(LINUX_DIR)/$(USER_UNIT) $(USER_UNIT_DIR)/$(USER_UNIT)
	$(SYSTEMCTL_USER) daemon-reload
	$(SYSTEMCTL_USER) enable $(USER_UNIT)
	@echo "installed; 'make start-user' once ~/.config/homedrive/config.yaml and the rclone remote exist"
else
	$(error make install-user is only supported on Linux)
endif

# Daily loop: build, atomically replace the binary, restart. Does not
# reinstall the unit (use install-user for that).
deploy-local: user-guard install-user-binary
	@test -f $(USER_UNIT_DIR)/$(USER_UNIT) || { echo "user unit not installed; run 'make install-user' first" >&2; exit 1; }
	$(SYSTEMCTL_USER) restart $(USER_UNIT)

start-user restart-user: user-guard
	$(SYSTEMCTL_USER) $(patsubst %-user,%,$@) $(USER_UNIT)

stop-user:
	$(SYSTEMCTL_USER) stop $(USER_UNIT)

status-user:
	$(SYSTEMCTL_USER) status $(USER_UNIT) --no-pager

logs-user:
	journalctl --user -u $(USER_UNIT) -f

# Leaves config, state/journal, audit log and the rclone config untouched.
uninstall-user:
	-$(SYSTEMCTL_USER) disable --now $(USER_UNIT)
	rm -f $(USER_UNIT_DIR)/$(USER_UNIT) $(USER_BIN_DIR)/$(BINARY)
	$(SYSTEMCTL_USER) daemon-reload

clean:
	rm -rf $(DIST)
