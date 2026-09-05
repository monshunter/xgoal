.PHONY: verify-m0 verify-m1 verify-m2 verify-m3 verify-m4 verify-m5 verify-m6 fmt-check test shuffle race vet cli-smoke sqlite-cross-build m2-failure-matrix m2-cross-build m3-contract m3-real-smoke m4-contract m4-real-smoke m5-safety m5-cross-build m6-release m6-cross-build

verify-m0: fmt-check test race vet cli-smoke

verify-m1: fmt-check test shuffle race vet cli-smoke sqlite-cross-build

verify-m2: fmt-check test m2-failure-matrix shuffle race vet cli-smoke sqlite-cross-build m2-cross-build

verify-m3: verify-m2 m3-contract m3-real-smoke

verify-m4: verify-m3 m4-contract m4-real-smoke

verify-m5: fmt-check test shuffle race vet cli-smoke sqlite-cross-build m2-cross-build m3-contract m4-contract m5-safety m5-cross-build

verify-m6: fmt-check test shuffle race vet cli-smoke sqlite-cross-build m2-failure-matrix m2-cross-build m3-contract m4-contract m5-safety m5-cross-build m6-release m6-cross-build

fmt-check:
	sh scripts/xgoal/gofmt-check.sh

test:
	go test ./...

shuffle:
	go test -shuffle=on -count=20 -timeout=90m ./...

race:
	go test -race ./...

vet:
	go vet ./...

cli-smoke:
	go run ./cmd/xgoal --help >/dev/null
	go run ./cmd/xgoal goal replan --help >/dev/null
	go run ./cmd/xgoal version
	go run ./cmd/xgoal config validate --file xgoal.example.yaml
	for shell_name in bash zsh fish powershell; do go run ./cmd/xgoal completion "$$shell_name" >/dev/null; done

sqlite-cross-build:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go test -c -o /dev/null ./internal/store/sqlite
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o /dev/null ./internal/store/sqlite

m2-failure-matrix:
	go test ./internal/patch -run 'TestCapturePreservesEveryFilesystemChangeWithoutMovingHeadOrIndex|TestReplayRejectsConflictAndScopeViolationBeforeMutation|TestReplayRejectsSymlinkParentBeforeMutation' -count=1
	go test ./internal/store/sqlite -run 'TestPromotionPreflightRejectsStaleEvidenceAndFailureClosesEffect|TestM2ArtifactsPersistAndFailClosedOnDiskTamperingAcrossRestart|TestM2MigrationUpgradesM1StoreAndPreservesStateWithBackup' -count=1
	go test ./internal/promotion -run 'TestPromotionRecoversAfterRefUpdateWithoutCreatingDuplicateCommit' -count=1

m2-cross-build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -exec=true ./...

m3-contract:
	go test ./internal/adapter/... ./internal/redact ./internal/workpacket -count=1

m3-real-smoke:
	XGOAL_RUN_CODEX_SMOKE=1 go test ./internal/adapter/codex -run '^TestM3RealCodexFastAndStandardImplementer$$' -count=1 -v

m4-contract:
	go test ./internal/adapter/... ./internal/protocol ./internal/review ./internal/store/sqlite -count=1

m4-real-smoke:
	XGOAL_RUN_CROSS_REVIEW_SMOKE=1 go test ./internal/review -run '^TestM4RealCrossProviderReview$$' -count=1 -v

m5-safety:
	go test ./internal/reconcile ./internal/policy -count=1
	go test ./internal/store/sqlite -run 'TestGateAuthorizationIsScopedFiniteAndAtomic|TestExpiredAuthorizationFailsClosedAndPersistsExpiry|TestFailureRecordsSurviveRestart|TestWorkerRecoveryResolutionRevokesLeaseAndReconcilesWork|TestGoalStatusProjectsActiveRuntimeFactsWithoutNestedQueryDeadlock' -count=1
	go test ./internal/api ./internal/daemon ./internal/recovery ./internal/app -count=1

m5-cross-build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -exec=true ./internal/api ./internal/app ./internal/control ./internal/daemon ./internal/policy ./internal/reconcile ./internal/recovery
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go test -c -o /dev/null ./internal/daemon

m6-release:
	go test ./internal/planner ./internal/goalcompile ./internal/orchestrator ./internal/finalize ./internal/report ./internal/benchmark ./internal/projectinit ./internal/app -count=1
	go run ./cmd/xgoal benchmark validate --file benchmarks/suite.json

m6-cross-build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/xgoal
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /dev/null ./cmd/xgoal
