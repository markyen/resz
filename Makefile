.PHONY: build clean

build: resz

resz:
	go build -o $@ ./cmd/resz

clean:
	rm -f resz
