PLUGIN_ID := codex-upstream-switcher
VERSION ?= 0.1.0
DIST_DIR ?= dist

.PHONY: test build clean

test:
	go test ./...

build:
	go build -trimpath -buildmode=c-shared \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o $(DIST_DIR)/$(PLUGIN_ID).so .
	rm -f $(DIST_DIR)/$(PLUGIN_ID).h

clean:
	rm -rf $(DIST_DIR)
