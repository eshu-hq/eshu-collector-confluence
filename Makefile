# Local parity for CI: `make ci` runs the lint, test, and race commands.
# It is the whole local contract; CI adds coverage, shell, image, govulncheck,
# and gosec lanes around the same commands.

.PHONY: ci lint test race

COVERAGE_FLOOR ?= 90

ci: lint test race

lint:
	go vet ./...
	golangci-lint run ./...
	golangci-lint fmt --diff ./...
	go mod tidy -diff
	git diff --exit-code -- go.mod go.sum
	go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/*.yml
	over=$$(find . -name '*.go' -not -path './vendor/*' -exec wc -l {} + | awk '$$1 > 500 && $$2 != "total" {print}'); \
	if [ -n "$${over}" ]; then echo "$${over}"; echo "FAIL: .go files over 500 lines"; exit 1; fi

test:
	go build ./...
	go test ./... -count=1

race:
	go test ./... -race -count=1 -covermode=atomic -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -n 30
	total=$$(go tool cover -func=coverage.out | awk '/^total:/ {gsub(/%/, "", $$3); print $$3}'); \
	echo "total=$${total} floor=$(COVERAGE_FLOOR)"; \
	awk -v t="$${total}" -v f="$(COVERAGE_FLOOR)" 'BEGIN { if (t + 0 < f + 0) { print "FAIL: coverage below floor"; exit 1 } }'
