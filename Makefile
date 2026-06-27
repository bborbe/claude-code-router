include Makefile.variables
include Makefile.precommit

SERVICE = bborbe/claude-code-router
LISTEN ?= 127.0.0.1:8788

run:
	@go run -mod=mod main.go -listen=$(LISTEN) -v=2

deps:
	go install github.com/onsi/ginkgo/v2/ginkgo@v2.25.3

.PHONY: fix
fix:
	@for dir in $$(find `pwd` -type d -name vendor -prune -o -name go.mod -exec dirname "{}" \; | grep -v '^$$'); do \
		cd $${dir}; \
		echo "fix $${dir}"; \
		go get github.com/go-git/go-git/v5@latest; \
		go get github.com/containerd/containerd@latest; \
		go get golang.org/x/crypto@latest; \
		go get golang.org/x/net@latest; \
	done
