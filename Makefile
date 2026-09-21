VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)

PREFIX ?= /usr/local
SBINDIR ?= $(PREFIX)/sbin
# OpenBSD and FreeBSD < 14: $(PREFIX)/man; FreeBSD >= 14: $(PREFIX)/share/man
MANDIR ?= $(PREFIX)/man
INSTALL ?= install

.PHONY: build test vet build-all build-openbsd build-freebsd build-linux \
        install uninstall man clean

build:
	go build -ldflags="$(LDFLAGS)" -o tower .

test:
	go vet ./...
	go test -race ./...

build-all: build-openbsd build-freebsd build-linux

build-openbsd:
	CGO_ENABLED=0 GOOS=openbsd GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o tower-openbsd-amd64 .

build-freebsd:
	CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o tower-freebsd-amd64 .

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o tower-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o tower-linux-arm64 .

install: build
	$(INSTALL) -d $(DESTDIR)$(SBINDIR) $(DESTDIR)$(MANDIR)/man8
	$(INSTALL) -m 755 tower $(DESTDIR)$(SBINDIR)/tower
	$(INSTALL) -m 444 tower.8 $(DESTDIR)$(MANDIR)/man8/tower.8

uninstall:
	rm -f $(DESTDIR)$(SBINDIR)/tower $(DESTDIR)$(MANDIR)/man8/tower.8

# Check and display the manual page
man:
	mandoc -Tlint tower.8 || true
	mandoc -Tascii tower.8 | $${PAGER:-less}

clean:
	rm -f tower tower-*
