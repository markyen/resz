.PHONY: build clean

build: resz

# Phony so the binary is always rebuilt: listing every .go file as a
# prerequisite is the only alternative, and `make build` skipping a
# rebuild silently tests a stale binary.
.PHONY: resz
resz:
	go build -o $@ ./cmd/resz

clean:
	rm -f resz
