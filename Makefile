.PHONY: verify-m0 verify-m1 verify-m2 verify-m3 verify-m4 verify-m5 verify-m6 fmt-check test shuffle race vet cli-smoke sqlite-cross-build m2-failure-matrix m2-cross-build m3-contract m3-real-smoke m4-contract m4-real-smoke m5-safety m5-cross-build m6-release m6-cross-build benchmark-validate

# Keep local verification usable alongside the desktop and project services.
# Export both limits so test-launched Go builds inherit them. Tests using
# t.Parallel default to GOMAXPROCS; neither setting is a host-wide CPU quota.
.NOTPARALLEL:
GOMAXPROCS ?= 2
GO_PACKAGE_PARALLEL ?= 1
export GOMAXPROCS
override GOFLAGS := $(GOFLAGS) -p=$(GO_PACKAGE_PARALLEL)
export GOFLAGS

# Repeat concurrency/state contracts, not every external-process scenario.
SHUFFLE_STORE_TESTS := TestConcurrentClaimsAcrossWorkItemsCreateOnlyOneProjectLease \
	TestClaimWorkCreatesAttemptLeaseAndEventsAtomically \
	TestClaimWorkRollsBackAttemptLeaseAndWorkWhenEventFails \
	TestLeaseHeartbeatExpiryGenerationAndLateWriteIsolation \
	TestConcurrentIdempotencyBeginsHaveOneCreatorAndOneIdentity \
	TestIdempotencyRecordReplaysSameRequestAndRejectsMismatch \
	TestEffectJournalDeduplicatesRequestAndRequiresObservation \
	TestEffectRequestRollsBackWhenEventFails \
	TestGatePersistsBoundedDecisionAndReopens \
	TestRequiredGateDecisionAndExpiryFenceSchedulingAndCompletion \
	TestInvocationIndexIsImmutableCASAndDoesNotChangeGoal

verify-m0: fmt-check test race vet cli-smoke

verify-m1: fmt-check test shuffle race vet cli-smoke sqlite-cross-build

verify-m2: fmt-check test m2-failure-matrix shuffle race vet cli-smoke sqlite-cross-build m2-cross-build

verify-m3: verify-m2 m3-contract m3-real-smoke

verify-m4: verify-m3 m4-contract m4-real-smoke

verify-m5: fmt-check test shuffle race vet cli-smoke sqlite-cross-build m2-cross-build m3-contract m4-contract m5-safety m5-cross-build

# race executes every test once, including all CLI/service/failure scenarios.
# Historical matrix/contract targets remain available for targeted diagnosis.
verify-m6: fmt-check race shuffle vet cli-smoke sqlite-cross-build m2-cross-build benchmark-validate m6-cross-build

fmt-check:
	sh scripts/xgoal/gofmt-check.sh

test:
	go test -count=1 -timeout=15m ./...

shuffle:
	go test -shuffle=on -count=20 -timeout=2m ./internal/canonical ./internal/domain ./internal/kernel ./internal/protocol ./internal/evidence ./internal/reconcile ./internal/policy ./internal/scope
	@set -eu; \
	available=$$(go test -list '^Test' ./internal/store/sqlite); \
	for test_name in $(SHUFFLE_STORE_TESTS); do \
		printf '%s\n' "$$available" | grep -Fx "$$test_name" >/dev/null || { printf 'Missing shuffle test: %s\n' "$$test_name" >&2; exit 1; }; \
	done; \
	test -n '$(strip $(SHUFFLE_STORE_TESTS))'; \
	pattern=$$(printf '%s|' $(SHUFFLE_STORE_TESTS)); \
	go test -shuffle=on -count=20 -timeout=2m -run "^($${pattern%|})$$" ./internal/store/sqlite

race:
	go test -race -count=1 -timeout=20m ./...

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

m6-release: benchmark-validate
	go test ./internal/planner ./internal/goalcompile ./internal/orchestrator ./internal/finalize ./internal/report ./internal/benchmark ./internal/projectinit ./internal/app -count=1

benchmark-validate:
	go run ./cmd/xgoal benchmark validate --file benchmarks/suite.json

m6-cross-build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/xgoal
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /dev/null ./cmd/xgoal
