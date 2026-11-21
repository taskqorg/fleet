# LLM Code Benchmarks (AI Coding Daily Tracks)

Benchmark tracks adapted from AI Coding Daily evaluations for [Fleet Lab](../designs/fleet-lab.md). Each track has a stable `<a id="...">` anchor indexed in [`config/lab/use-cases/llm-code-benchmarks.json`](../../config/lab/use-cases/llm-code-benchmarks.json) and monitored for drift via `node scripts/lab-drift.ts`.

## Evaluation methodology

- **Existing codebases:** tasks run inside realistic, pre-existing modules and repositories rather than greenfield snippets, so agents must respect surrounding conventions, interfaces and constraints.
- **Single-agent, no-coaching runs:** each trial receives one fixed prompt and workspace snapshot with no mid-run human hints, follow-up steering or interactive coaching.
- **Repeated runs and consistency spread:** variants are evaluated across multiple independent repeats (`repeats >= 3` in calibration runs) to measure pass rate, variance (noise floor), wall-clock median and cost spread.

<a id="laravel-code-quality"></a>

## Laravel Code Quality

Source: this section; Lab registry entry `bench:laravel-code-quality`.

- **Task shape:** Refactor or extend an existing Laravel / PHP service, controller or Eloquent query path while preserving routes, database semantics and tests.
- **What is measured:** Architectural cleanliness (service/repository boundaries, validation, N+1 query avoidance), adherence to framework idioms and zero regression in existing behavior.
- **Checks:** Deterministic PHP/script verification and code-quality/review checks against expected invariants.

<a id="react-ts-code-quality"></a>

## React and TypeScript Code Quality

Source: this section; Lab registry entry `bench:react-ts-code-quality`.

- **Task shape:** Implement or refactor a stateful component or hook module inside an existing React + TypeScript codebase under strict type checking.
- **What is measured:** Type soundness (no `any` leaks or unsafe casts), clean hook dependencies, predictable state transitions, accessibility semantics and bundle/render discipline.
- **Checks:** TypeScript compilation, deterministic behavior assertions and component contract checks.

<a id="bug-finding"></a>

## Bug Finding

Source: this section; Lab registry entry `bench:bug-finding`.

- **Task shape:** Diagnose a subtle, realistic defect in an existing module (off-by-one boundaries, stale state, race/ordering errors, faulty edge-case handling or incorrect arithmetic) and deliver a minimal fix.
- **What is measured:** Root-cause accuracy, minimal surface-area patch, preservation of passing callers and elimination of the failing edge case.
- **Checks:** Deterministic regression suite (`node` / language runner) that fails on the unpatched input and passes only when the exact defect is resolved (`config/lab/examples/bench-bug-finding-ts.json`).

<a id="python-pricing-engine"></a>

## Python Pricing Engine

Source: this section; Lab registry entry `bench:python-pricing-engine`.

- **Task shape:** Refactor and extend a procedural Python pricing prototype into composable rule families (volume tiers, promotional discounts, stackability, tax/rounding) with exact numeric precision.
- **What is measured:** Extensibility of the refactored rule architecture, exact `Decimal`/cent arithmetic without floating-point drift and deterministic handling of rule ordering and conflicts.
- **Checks:** Deterministic `python3` verification suite covering rule composition, stacking boundaries and rounding invariants (`config/lab/examples/bench-pricing-engine-py.json`).

<a id="edge-case-testing"></a>

## Edge-Case Testing

Source: this section; Lab registry entry `bench:edge-case-testing`.

- **Task shape:** Write a focused test suite for an existing module that exposes boundary, empty/null, concurrency, overflow or malformed-input defects without modifying unrelated production code.
- **What is measured:** Fault detection rate across seeded mutants/edge cases, clarity and determinism of test assertions and zero flaky timing dependencies.
- **Checks:** Deterministic test-runner execution verifying that generated tests fail against buggy implementations and pass against the reference implementation.
