PKG ?= ./...
GO_TEST_ENV := CLASHFX_DISABLE_OS_SYSPROXY=1 CLASHFX_DISABLE_LAUNCH_AGENT=1 CLASHFX_DISABLE_LEGACY_PLIST_MIGRATION=1 CLASHFX_DISABLE_PRIVILEGED_SERVICE=1
UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)
SWIFT_TARGET := $(if $(filter arm64,$(UNAME_M)),arm64-apple-macos12.0,x86_64-apple-macos12.0)

.PHONY: help check-js test-go test-go-race typecheck-macos check-fast build-macos smoke-macos build-win-daemon verify-release

help:
	@echo "ClashFX development targets:"
	@echo "  make check-fast [PKG=./internal/...]  Fast JS syntax + Go test + Swift typecheck (<10s)"
	@echo "  make smoke-macos                      Build/stage macOS app + isolated DOM smoke test"
	@echo "  make build-win-daemon                 Cross-build Windows arm64/amd64 daemon & service"
	@echo "  make verify-release                   Full -race Go suite + macOS app + compatibility gates"

check-js:
	@for f in windows/ui-qt/web/*.js; do node --check "$$f" || exit 1; done
	@echo "[OK] Web SPA JS syntax check passed"

test-go:
	cd core-engine && $(GO_TEST_ENV) go test -count=1 $(PKG)

test-go-race:
	cd core-engine && $(GO_TEST_ENV) go test -race -count=1 $(PKG)

typecheck-macos:
ifeq ($(UNAME_S),Darwin)
	xcrun swiftc -typecheck -sdk "$$(xcrun --sdk macosx --show-sdk-path)" -target $(SWIFT_TARGET) -framework Cocoa -framework WebKit -framework Carbon macos/ui-macos/Sources/*.swift
else
	@echo "Skipping macOS Swift typecheck on $(UNAME_S)"
endif

check-fast: check-js test-go typecheck-macos

build-macos:
	python3 macos/scripts/build-daemon.py
	python3 macos/scripts/build-ui.py
	python3 macos/scripts/stage-app.py
	python3 scripts/verify-release-compatibility.py --app macos/dist/ClashFX.app

smoke-macos: build-macos
	@TMP_DIR=$$(mktemp -d /tmp/clashfx-smoke.XXXXXX); \
	trap "rm -rf '$$TMP_DIR'" EXIT; \
	$(GO_TEST_ENV) ./macos/dist/ClashFX.app/Contents/MacOS/ClashFX \
		--pipe "$$TMP_DIR/d.sock" \
		--data-dir "$$TMP_DIR/data" \
		--smoke-test-ms 4500 \
		--smoke-report "$$TMP_DIR/smoke.json"; \
	python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d.get("ui_ready") and d.get("daemon_connected") and d.get("dom",{}).get("ok"), d; print("[OK] macOS smoke passed:", len(d["dom"]["steps"]), "DOM steps")' "$$TMP_DIR/smoke.json"

build-win-daemon:
	python3 windows/scripts/build-daemon.py

verify-release: check-js test-go-race smoke-macos
	bash scripts/verify-dashboard-compatibility-bundle.sh macos/dist/ClashFX.app
