.PHONY: verify-m0 verify-m1 fmt-check test shuffle race vet cli-smoke sqlite-cross-build

verify-m0: fmt-check test race vet cli-smoke

verify-m1: fmt-check test shuffle race vet cli-smoke sqlite-cross-build

fmt-check:
	sh scripts/xgoal/gofmt-check.sh

test:
	go test ./...

shuffle:
	go test -shuffle=on -count=20 ./...

race:
	go test -race ./...

vet:
	go vet ./...

cli-smoke:
	go run ./cmd/xgoal version
	go run ./cmd/xgoal config validate --file xgoal.example.yaml

sqlite-cross-build:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go test -c -o /dev/null ./internal/store/sqlite
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o /dev/null ./internal/store/sqlite
