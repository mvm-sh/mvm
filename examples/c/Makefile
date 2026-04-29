# Build a small C program that embeds the mvm interpreter.
#
#   make        # build libmvm.a and the demo binary
#   make run    # build and run
#   make clean  # remove generated files
#
# Caveat: Go's c-archive build mode does not work reliably with musl libc.
# Use a glibc-based Linux distribution or macOS to run the demo.

UNAME_S := $(shell uname -s)

ifeq ($(UNAME_S),Linux)
  LDFLAGS := -lpthread -ldl
endif
ifeq ($(UNAME_S),Darwin)
  LDFLAGS := -framework CoreFoundation -framework Security
endif

CFLAGS := -O2 -Wall

.PHONY: all run clean

all: demo

libmvm.a libmvm.h: shim/libmvm.go
	go build -buildmode=c-archive -o libmvm.a ./shim

demo: demo.c libmvm.a libmvm.h
	$(CC) $(CFLAGS) -o $@ demo.c libmvm.a $(LDFLAGS)

run: demo
	./demo

clean:
	rm -f libmvm.a libmvm.h demo
