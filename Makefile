# Workstation builds of fltr, for `fltr inspect`, `fltr protocols` and the
# tests. The device image is built by `ramekin build` from fltr.def, which
# does all of this inside its build stage.
#
#   make ndpi            build third_party/ndpi/<arch>/lib/libndpi.a
#   make                 build bin/fltr-{amd64,arm64} and bin/gk-network-{amd64,arm64}
#   make test            run the tests (amd64 host)
#
# fltr is fully static, with zig as the C (cross) compiler for nDPI;
# gk-network is pure Go.

ARCHS     ?= amd64 arm64
zigtarget  = $(if $(filter amd64,$(1)),x86_64-linux-musl,aarch64-linux-musl)
LDFLAGS    = -s -w -linkmode=external "-extldflags=-static -s"

.PHONY: all ndpi test clean $(ARCHS:%=bin/fltr-%) $(ARCHS:%=bin/gk-network-%)

all: $(ARCHS:%=bin/fltr-%) $(ARCHS:%=bin/gk-network-%)

ndpi:
	scripts/build-ndpi.sh $(ARCHS)

$(ARCHS:%=bin/fltr-%): bin/fltr-%:
	CGO_ENABLED=1 GOOS=linux GOARCH=$* CC="zig cc -target $(call zigtarget,$*)" \
		go build -trimpath -ldflags '$(LDFLAGS)' -o $@ .

$(ARCHS:%=bin/gk-network-%): bin/gk-network-%:
	CGO_ENABLED=0 GOOS=linux GOARCH=$* go build -trimpath -ldflags '-s -w' -o $@ ./cmd/gk-network

test:
	CGO_ENABLED=1 CC="zig cc -target x86_64-linux-musl" go test -ldflags '$(LDFLAGS)' ./...

clean:
	rm -rf bin third_party/ndpi
