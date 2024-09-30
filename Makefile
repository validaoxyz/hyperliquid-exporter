.PHONY: all build install clean

BUILD_DIR := bin
BINARY_NAME := hl_exporter
# Allow custom installation path; default to /usr/local/bin
INSTALL_PREFIX ?= /usr/local
INSTALL_BIN_DIR := $(INSTALL_PREFIX)/bin

all: build

build:
	@echo "Building $(BINARY_NAME)..."
	mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/hl-exporter

install: build
	@echo "Installing $(BINARY_NAME) to $(INSTALL_BIN_DIR)"
	install -Dm0755 $(BUILD_DIR)/$(BINARY_NAME) $(INSTALL_BIN_DIR)/$(BINARY_NAME)

clean:
	@echo "Cleaning up..."
	rm -rf $(BUILD_DIR)
