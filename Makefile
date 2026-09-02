.PHONY: verify-m0 verify-m1 verify-m2 fmt-check test shuffle race vet cli-smoke sqlite-cross-build m2-failure-matrix m2-cross-build

verify-m0: fmt-check test race vet cli-smoke

verify-m1: fmt-check test shuffle race vet cli-smoke sqlite-cross-build

verify-m2: fmt-check test m2-failure-matrix shuffle race vet cli-smoke sqlite-cross-build m2-cross-build

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

m2-failure-matrix:
	go test ./internal/patch -run 'TestCaptureIgnoresAgentHistoryAndPreservesEveryFilesystemChange|TestReplayRejectsConflictAndScopeViolationBeforeMutation|TestReplayRejectsSymlinkParentBeforeMutation' -count=1
	go test ./internal/store/sqlite -run 'TestPromotionPreflightRejectsStaleEvidenceAndFailureClosesEffect|TestM2ArtifactsPersistAndFailClosedOnDiskTamperingAcrossRestart|TestM2MigrationUpgradesM1StoreAndPreservesStateWithBackup' -count=1
	go test ./internal/promotion -run 'TestPromotionRecoversAfterRefUpdateWithoutCreatingDuplicateCommit' -count=1

m2-cross-build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -exec=true ./...
