# Architecture guards

The build-failing structural tier in `internal/arch/`. **50 guards across 15 files.**

A guard's strength is not what it asserts but **how it decides what to assert it over** — its
subject set. A guard whose subjects are a written list pins only the instances someone
remembered; a guard that derives its subjects from the schema or the tree covers the ones
nobody has thought of yet. This programme reopened three classes for exactly that reason.

This file records, per guard, where its subject set comes from.

## Derivation levels

Levels as defined by the Trust Register.

| Level | Subject set comes from |
|---|---|
| **L0** | a comment or convention |
| **L1** | an integration test |
| **L2** | AST/source analysis over a **written list** of files or symbols |
| **L3** | a **whole-tree walk** |
| **L4** | a **registry** in the code (`TenantOwnedTables`, `platformInvariants`, a directory listing) |
| **L5** | the **schema** — read from the migration SQL |

**L2 is the level that has failed in this repository.** It is legitimate as a *pin* on a known
instance, and insufficient as the *only* enforcement of a class.

---

## Inventory

### Completeness guards — derive their own subjects

| File | Guard | Class defended | Derivation | Level |
|---|---|---|---|---|
| `queue_completeness_test.go` | `TestEveryWorkQueue_HasAFencingToken` | A claimed queue without a fencing token | Migration SQL: any table whose `status` defaults to `pending` | **L5** |
| `queue_completeness_test.go` | `TestEveryQueueClaim_IsAtomicAndFenced` | Non-atomic claim; unfenced outcome write | The queue set above, per function body | **L5** |
| `queue_completeness_test.go` | `TestEveryCursor_IsWrittenMonotonically` | Non-monotonic cursor | Migration SQL: cursor tables | **L5** |
| `queue_completeness_test.go` | `TestPrivilegedMutations_AreScopedToAnOwner` | Privileged mutation keyed only on caller-supplied ids | `TenantOwnedTables` registry | **L4** |
| `invariant_registration_test.go` | `TestEveryValidator_IsRegisteredAsAnInvariant` | A validator enforced at only one point | `platformInvariants` registry + tree walk of `store/postgres` | **L4** |
| `invariant_registration_test.go` | `TestPlatformInvariants_AreEnforcedAtBothPoints` | Boundary verified only at boot | `platformInvariants` registry | **L4** |
| `invariant_registration_test.go` | `TestCheckAll_ShortCircuitsInOrder` | Invariant ordering | `platformInvariants` registry | **L4** |
| `wiring_test.go` | `TestOperationalBinariesAreRegistered` | Unregistered privileged binary | `os.ReadDir("../../cmd")` | **L4** |
| `wiring_test.go` | `TestOperationalScriptsAreRegistered` | Unregistered privileged script | `os.ReadDir("../../scripts")` | **L4** |
| `scope_completeness_test.go` | `TestEveryTenantRegistryRoute_RequiresPlatformAdmin` | Platform operation under tenant scope | Whole-tree walk + justification map | **L3** |
| `scope_completeness_test.go` | `TestEveryQueuedRowProducer_UsesDerivedIdentity` | Non-deterministic row identity | Whole-tree walk | **L3** |
| `scope_completeness_test.go` | `TestEveryWorkerOutcomeWrite_UsesADetachedContext` | Outcome lost when the worker is stopped | Whole-tree walk | **L3** |
| `scope_completeness_test.go` | `TestEveryWorkerRunLoop_ObservesCancellation` | Demoted leader keeps running workers | Whole-tree walk | **L3** |
| `scope_completeness_test.go` | `TestAuditAppend_TakesTheChainHeadLock` | Split-brain audit authority | Whole-tree walk | **L3** |
| `scope_completeness_test.go` | `TestCreateDTOs_DoNotExposeEntityIdentity` | Client-supplied entity identity | Whole-tree walk | **L3** |
| `scope_completeness_test.go` | `TestMetricsIsNotOnThePublicListenerInProduction` | Metrics on the public listener | Whole-tree walk | **L3** |
| `ssrf_client_test.go` | `TestNoUnguardedOutboundHTTP` | Unguarded outbound HTTP client | `filepath.Walk` over every non-test `.go` file | **L3** |
| `httpapi/contract_test.go`¹ | `TestOpenAPIContract_CoversEveryServedRoute` | Undocumented API surface | `chi.Walk` over the live router | **L3** |
| `httpapi/contract_test.go`¹ | `TestOpenAPIContract_PromisesNothingItDoesNotServe` | Contract promising an unserved route | `chi.Walk` over the live router | **L3** |

¹ Lives in `internal/httpapi` because it walks the router, not in `internal/arch`. Listed here
because it is a structural build-failing guard and belongs to this tier.

### Instance pins — subject set is a written list

**Each is L2. None is the sole enforcement of its class** — the *Backed by* column names the
completeness guard that covers the same class.

| File | Guard | Class defended | Backed by |
|---|---|---|---|
| `cursor_monotonicity_test.go` | `TestCursorWriters_AreMonotonic` | Non-monotonic cursor | `TestEveryCursor_IsWrittenMonotonically` (L5) |
| `lease_fencing_test.go` | `TestMarkResult_IsFencedOnTheClaim` | Unfenced completion by an evicted worker | `TestEveryQueueClaim_IsAtomicAndFenced` (L5) |
| `producer_identity_test.go` | `TestProducers_UseDeterministicRowIdentity` | Non-deterministic row identity | `TestEveryQueuedRowProducer_UsesDerivedIdentity` (L3) |
| `ssrf_client_test.go` | `TestOutboundClients_AreSSRFGuarded` | SSRF via tenant-supplied URLs | `TestNoUnguardedOutboundHTTP` (L3) |
| `retry_accounting_test.go` | `TestWorkerOutcomeWrites_AreNotCancelledByDemotion` | Outcome lost on demotion | `TestEveryWorkerOutcomeWrite_UsesADetachedContext` (L3) |
| `retry_accounting_test.go` | `TestOutcomeContext_IsDetachedAndBounded` | Outcome write on a cancellable context | `TestEveryWorkerOutcomeWrite_UsesADetachedContext` (L3) |
| `retry_accounting_test.go` | `TestClaimDue_ChargesTheAttemptAndBoundsIt` | Unbounded retry of unrecorded attempts | ⚠️ **none — see Flagged** |
| `retry_accounting_test.go` | `TestWorkers_ReleaseUnusedClaimsOnStop` | Claim budget burned on shutdown | ⚠️ **none — see Flagged** |
| `delivery_destination_test.go` | `TestDelivery_RecordsItsOwnDestination` | Historical record derived from mutable state | ⚠️ **none — see Flagged** |
| `delivery_destination_test.go` | `TestDeliveryDestination_IsWrittenWithTheOutcome` | Destination written outside the fenced write | ⚠️ **none** |
| `delivery_destination_test.go` | `TestDispatcher_RecordsTheURLItPosted` | Destination not captured at attempt time | ⚠️ **none** |
| `delivery_destination_test.go` | `TestDeliveryReads_DoNotDeriveDestinationFromTheWebhook` | Read-time join to mutable state | ⚠️ **none** |
| `delivery_destination_test.go` | `TestDeliveryView_DoesNotMintATimestamp` | Signature minted at read time | ⚠️ **none** |
| `constitutional_metadata_test.go` | `TestMetadataWrites_AreGuardedAtTheStorageChokepoint` | §9.1 inputs via the metadata channel | chokepoint² |
| `constitutional_metadata_test.go` | `TestMetadataClear_PreservesConstitutionalInputs` | Wholesale clear erasing §9.1 inputs | chokepoint² |
| `constitutional_metadata_test.go` | `TestInception_RefusesConstitutionalMetadata` | §9.1 inputs at create/bulk | chokepoint² |
| `constitutional_metadata_test.go` | `TestConstitutionalMetadataKeys_HaveOneDefinition` | Two definitions of the key set | chokepoint² |
| `runtime_invariant_test.go` | `TestSchemaValidator_IsRunContinuouslyNotOnlyAtStartup` | Boundary verified only at boot | `TestPlatformInvariants_AreEnforcedAtBothPoints` (L4) |
| `runtime_invariant_test.go` | `TestInvariantBreach_FailsClosedOnEveryTenantDataPath` | Breach not failing closed | `TestPlatformInvariants_AreEnforcedAtBothPoints` (L4) |
| `runtime_invariant_test.go` | `TestSentinel_TreatsUnverifiedAsUnhealthy` | Unverified read as healthy | `TestPlatformInvariants_AreEnforcedAtBothPoints` (L4) |
| `wiring_test.go` | `TestServerWiringUsesTenantScopedPool` · `TestWiringExemptionsRemainJustified` · `TestPlatformRoutesRequirePlatformAdmin` · `TestWorkersFanOutThroughOwnershipFramework` · `TestExecutionConsumersReDeriveOwnership` · `TestProducersHaveNoOutboundCapability` · `TestWorkersStartOnlyUnderLeadership` · `TestDatabaseDroppingScripts_RefuseTheLiveDatabase` | Composition-root wiring; ownership re-derivation; DR-drill safety | composition root is a single file; the registry guards above cover the sets |

² **Structural, not enumerated.** These assert that every metadata write passes one storage
chokepoint — you cannot reach the column another way — which is a stronger property than a swept
list. Recorded as L2 by mechanism, structural by effect.

### Dependency rules

| File | Guard | Class defended | Derivation | Level |
|---|---|---|---|---|
| `deps_test.go` | `TestDependencyRules` | Layering violation (ADR-ARCH-001) | Whole-tree import walk against declared rules | **L3** |
| `deps_logic_test.go` | `TestViolations` | Rule-engine correctness | Table of cases over the rule engine | n/a — tests the guard itself |

The rule *table* in `deps_test.go` is a written list by design: architectural layering **is** a
declared policy, not a discoverable property. The **subjects** it is applied to are every file in
the tree.

### Contract guards

| File | Guard | Class defended | Derivation | Level |
|---|---|---|---|---|
| `constitutional_destruction_test.go` | `TestConfigObjectRepository_ExposesNoDestructiveMethod` | Unaudited destruction via a second door | AST over the persistence contract | **structural** |
| `constitutional_destruction_test.go` | `TestConfigObjectRepo_HasNoUnguardedDelete` | Bare SQL delete | AST over the repository | **L2** |
| `constitutional_destruction_test.go` | `TestHTTPHandlers_DoNotDestroyObjectsDirectly` | Handler bypassing the engine | AST over the handler package | **L2** |

`TestConfigObjectRepository_ExposesNoDestructiveMethod` is the strongest form in this file: it
asserts the method cannot be **expressed**, so there is nothing to sweep for.

---

## ⚠️ Flagged — enumerated with no derived backing

Seven guards are the only enforcement of their class and take their subjects from a written list.

| Guard | Subjects listed |
|---|---|
| `TestClaimDue_ChargesTheAttemptAndBoundsIt` | `store/postgres/webhook_store.go`, `store/postgres/policy_store.go` |
| `TestWorkers_ReleaseUnusedClaimsOnStop` | `events/dispatcher.go`, `policy/executor.go` |
| `TestDelivery_RecordsItsOwnDestination` | `events/events.go`, `events/ports.go` |
| `TestDeliveryDestination_IsWrittenWithTheOutcome` | `store/postgres/webhook_store.go` |
| `TestDispatcher_RecordsTheURLItPosted` | `events/dispatcher.go` |
| `TestDeliveryReads_DoNotDeriveDestinationFromTheWebhook` | `store/postgres/webhook_store.go`, `store/postgres/webhook_consume_store.go` |
| `TestDeliveryView_DoesNotMintATimestamp` | `events/sign.go` |

**What this means, stated no wider than the evidence.** Each guard is correct and passing. Being
enumerated is not a defect — it predicts one. In this repository that prediction has been right
three times: a third queue, a third worker and a fifth privileged path each appeared after a
guard had named the two or four then known.

The **retry-accounting** pair is the closer match to that pattern: `webhook_delivery`,
`policy_execution` and `webhook_replay_job` are all claimed queues, and the queue set is already
derivable from the schema (`TestEveryWorkQueue_HasAFencingToken`), so a fourth queue would be
covered for fencing but **not** for retry accounting.

The **delivery-destination** five are narrower: Trust Register entry 25 is recorded **OPEN**, its
remaining instance is held under **AR-001**, and that review has not been implemented. Widening
these guards before AR-001's instance 2 lands would pin a shape that is expected to change.

**No guard is changed by this inventory.** These are observations for whoever schedules the work.

---

## Maintaining this file

It is written by hand and will drift. When adding a guard, add its row; when a guard's subject
set moves from a list to the schema, the tree or a registry, move its row and delete its
*Backed by* note.

**Counts to check against the code:** 15 files in `internal/arch/`, 50 guards there, plus 2 in
`internal/httpapi/contract_test.go`.
