OUT := ../internal/embed/clyde-inject.dylib
SRC := clyde_inject.c

.PHONY: build test clean

build:
	./build.sh

test: build
	./test.sh

clean:
	rm -f "$(OUT)"
