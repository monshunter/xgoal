.PHONY: verify-m0 fmt-check test race vet cli-smoke

verify-m0: fmt-check test race vet cli-smoke

fmt-check:
	sh scripts/xgoal/gofmt-check.sh

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

cli-smoke:
	go run ./cmd/xgoal version
	go run ./cmd/xgoal config validate --file xgoal.example.yaml
