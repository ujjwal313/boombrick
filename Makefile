GO      ?= go
VERSION ?= dev
PKG     := github.com/ujjwal313/boombrick
LDFLAGS := -s -w -X $(PKG)/internal/version.Version=$(VERSION)
# SQLite without loadable extensions, which Boombrick never loads.
TAGS    := sqlite_omit_load_extension

# QuickJS-NG, yt-dlp's JavaScript runtime: the project's static builds,
# checked against these sums.
QJS_VERSION     := v0.17.0
QJS_URL         := https://github.com/quickjs-ng/quickjs/releases/download/$(QJS_VERSION)
QJS_SHA_aarch64 := 3372133484edf50a69f3c67903af41206d22a061e930e3cfb63269272ef56d2e
QJS_SHA_x86_64  := 0bfc02511a9f549c28b53880d988fc7cd5d361e90c5e8afdfcd7dc6774ceace5

.PHONY: build run test fonts fixtures clean

build:
	$(GO) build -trimpath -tags '$(TAGS)' -ldflags '$(LDFLAGS)' -o dist/desktop/boombrick ./cmd/boombrick
	$(GO) build -trimpath -ldflags '-s -w' -o dist/desktop/bin/boombrick-spotify ./cmd/boombrick-spotify

build: dist/desktop/bin/qjs

dist/desktop/bin/qjs: dist/qjs/qjs-linux-$(shell uname -m)
	mkdir -p $(@D)
	cp $< $@

dist/qjs/qjs-linux-%:
	mkdir -p $(@D)
	curl -fsSLo $@.part $(QJS_URL)/qjs-linux-$*
	echo "$(QJS_SHA_$*)  $@.part" | sha256sum -c --quiet
	chmod +x $@.part
	mv $@.part $@

run: build
	BOOMBRICK_DATA=dist/desktop/data ./dist/desktop/boombrick

test:
	$(GO) test ./...

fonts:
	scripts/fonts.sh

fixtures:
	scripts/fixtures.sh

clean:
	rm -rf dist

DEVICE_IMAGE := boombrick-tg5040
GO_VERSION   := $(shell $(GO) env GOVERSION 2>/dev/null | sed 's/^go//')
PAK_DIR      := dist/pak/Tools/tg5040/Boombrick.pak
CROSSMIX_DIR := dist/crossmix/Apps/Boombrick
KNULLI_DIR   := dist/knulli/roms/ports/boombrick

.PHONY: device-image device pak crossmix knulli

device-image:
	docker build --load --build-arg GO_VERSION=$(GO_VERSION) -t $(DEVICE_IMAGE) build/docker

# Inside the container the checkout can belong to another user, which git
# refuses to read; the version comes from VERSION, not from git. The Spotify
# helper is built alongside.
device: device-image
	docker run --rm -v "$$(pwd)":/src \
		-v boombrick-gomod:/root/go/pkg/mod -v boombrick-gocache:/root/.cache/go-build \
		$(DEVICE_IMAGE) sh -c "go build -trimpath -buildvcs=false -tags '$(TAGS)' -ldflags '$(LDFLAGS)' -o dist/tg5040/boombrick ./cmd/boombrick && \
			go build -trimpath -buildvcs=false -ldflags '-s -w' -o dist/tg5040/boombrick-spotify ./cmd/boombrick-spotify"

# app fills the folder $(1) with what every firmware's app holds: the
# program, its launch script $(3) from build/$(2), QuickJS, the Spotify
# helper, the libraries the device lacks (Opus and Ogg for Boombrick; FLAC
# and mpg123, which go-librespot links, for the helper), and the licences.
define app
	rm -rf $(1)
	mkdir -p $(1)/bin
	cp dist/tg5040/boombrick build/$(2)/$(3) LICENSE NOTICE assets/fonts/OFL-*.txt $(1)/
	scripts/licenses.sh dist/tg5040/boombrick dist/tg5040/boombrick-spotify > $(1)/THIRD-PARTY.txt
	chmod +x $(1)/boombrick $(1)/$(3)
	cp dist/qjs/qjs-linux-aarch64 $(1)/bin/qjs
	cp dist/tg5040/boombrick-spotify $(1)/bin/
	cp third_party/ffmpeg/ffmpeg-aarch64 $(1)/bin/ffmpeg
	chmod +x $(1)/bin/ffmpeg
	docker run --rm -v "$$(pwd)":/src $(DEVICE_IMAGE) sh -c \
		'mkdir -p $(1)/lib && cd $$SYSROOT/usr/lib && cp -L libopus.so.0 libogg.so.0 libFLAC.so.8 libmpg123.so.0 \
			/src/$(1)/lib/'
endef

# Boombrick.pak.zip unzips at the root of the SD card, and is what Boombrick
# updates itself from. The Pak Store unzips its own zip into the pak's folder,
# so that one holds the same files at its root.
pak: device dist/qjs/qjs-linux-aarch64
	rm -rf dist/pak
	$(call app,$(PAK_DIR),pak,launch.sh)
	rm -f dist/Boombrick.pak.zip dist/Boombrick-PakStore.zip
	cd dist/pak && zip -9qr ../Boombrick.pak.zip Tools
	cd $(PAK_DIR) && zip -9qr $(CURDIR)/dist/Boombrick-PakStore.zip .
	@echo "dist/Boombrick.pak.zip dist/Boombrick-PakStore.zip"

# CrossMix lists the app in its Apps menu from config.json, with the icon
# drawn from the logo.
crossmix: device dist/qjs/qjs-linux-aarch64
	rm -rf dist/crossmix
	$(call app,$(CROSSMIX_DIR),crossmix,launch.sh)
	cp build/crossmix/config.json $(CROSSMIX_DIR)/
	$(GO) run ./scripts/appicon $(CROSSMIX_DIR)/icon.png
	rm -f dist/Boombrick-CrossMix.zip
	cd dist/crossmix && zip -9qr ../Boombrick-CrossMix.zip Apps
	@echo "dist/Boombrick-CrossMix.zip"

# Boombrick-Knulli.zip unzips at the root of Knulli's SHARE partition. Ports
# lists Boombrick.sh, which starts the app in the boombrick folder beside it.
knulli: device dist/qjs/qjs-linux-aarch64
	rm -rf dist/knulli
	$(call app,$(KNULLI_DIR),knulli,launch)
	cp build/knulli/Boombrick.sh dist/knulli/roms/ports/
	chmod +x dist/knulli/roms/ports/Boombrick.sh
	rm -f dist/Boombrick-Knulli.zip
	cd dist/knulli && zip -9qr ../Boombrick-Knulli.zip roms
	@echo "dist/Boombrick-Knulli.zip"
