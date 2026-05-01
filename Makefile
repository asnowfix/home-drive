.PHONY: build-mac build-arm64 build-amd64 test-linux test-pi deploy-pi clean

BINARY := homedrive
CMD    := ./homedrive/cmd/homedrive
DIST   := dist

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

test-linux:
	orb run -m dev -- go test -race ./homedrive/...

test-pi:
	ssh fix@nas.local 'cd /tmp/homedrive-test && go test ./...'

deploy-pi: build-arm64
	scp $(DIST)/$(BINARY)-arm64 fix@nas.local:/tmp/$(BINARY)
	ssh fix@nas.local 'sudo install /tmp/$(BINARY) /usr/local/bin/'
	ssh fix@nas.local 'sudo systemctl restart homedrive@fix.service'

clean:
	rm -rf $(DIST)
