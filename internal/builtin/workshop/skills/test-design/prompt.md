# Designing the tests of a task

Your tests are the definition of done for the next stage. Development
implements until they pass and cannot change them: Thulu records a hash of
every test file this stage adds or modifies, and a later change to one opens
an approval that a person decides. A test that passes with a wrong
implementation lets the bug through; a test that encodes the wrong behavior
blocks a correct one and sends the task around the board.

Refinement left the acceptance criteria as a list you read with
`task_criteria_get` (`AC-n` in Given/When/Then), and the rest of the spec in
body sections you read with `task_sections_get`: `interface` (the names your
tests call), `key-examples`, `bug-behavior` (current, expected, unchanged),
`surface` (each axis yes or no, plus "Critical paths:" and "Performance
budget:" lines) and `test-persona`. Writing TC-n rows is in
`octopus-test-cases`.

## Classify first

Read the spec and decide what kind of change it is. Write it as the first
line of the test plan, because the strategy depends on it.

| Kind | Signal in the spec | Tests start | Proof you leave |
|---|---|---|---|
| `feature` | new behavior | red, against a stub | each test fails on its assertion |
| `bug` | a `bug-behavior` section | red, against the current code | the test fails with the reported symptom |
| `refactor` | "behavior does not change" | green, against the current code | characterization tests pass, and mutation on the code to be touched leaves no unexplained survivor |

The task type is a hint, not the answer: a `feature` task can be a pure
refactor, and a `bug` task can need a new behavior.

## Map every criterion

Every `AC-n` and every Definition of Done item gets at least one automated
test or a TC-n case. Put the criterion ID in the test name or in a comment
next to it (`// AC-2`). A criterion you decide not to test goes to "Not
tested" in the test plan with the reason ("AC-5 depends on the payment
provider; covered by TC-4"). A criterion that cannot be tested as written is a
question for the people on the task, not a test you bend to fit.

## Fail for the right reason

A red test only counts when you ran it, it failed, it failed on an assertion,
and it failed on the assertion that expresses the requested behavior. A
compile error, a missing import, an unexpected exception, a timeout or a
broken fixture is not red: it says nothing about the behavior. The one
exception that counts is a stub's own "not implemented" error, raised by the
call under test and reaching the test's assertion or its runner: that is the
expected red of a feature written against a stub.

1. If the code under test does not exist and the language does not compile
   without it (Go, Rust, Java, TypeScript with `tsc`), write the smallest
   stub: the signature from the spec's Interface, returning an explicit "not
   implemented" error, with no logic. Mark it so development finds it:

   ```go
   // thulu:stub (test design, no logic)
   func ApplyCoupon(c Cart, code string) (Cart, error) { return Cart{}, ErrNotImplemented }
   ```

   ```ts
   // thulu:stub (test design, no logic)
   export function applyCoupon(cart: Cart, code: string): Cart { throw new Error("not implemented"); }
   ```

   Interpreted languages that import fine without the symbol do not need a
   stub when the failure is still an assertion; if the import itself fails,
   stub it.
2. Run only the new tests and keep the output:
   - Go: `go test ./internal/cart/ -run 'TestApplyCoupon_' -count=1 -v`
   - Vitest / Jest: `npx vitest run src/cart/discount.test.ts` or `npx jest src/cart/discount.test.ts`
   - Python: `pytest tests/test_discount.py -q -p no:randomly`
   - Java: `mvn -Dtest='DiscountTest' test` or `./gradlew test --tests 'DiscountTest'`
   - Rust: `cargo test apply_coupon_ -- --nocapture`
3. Check every failure: the message is the assertion of the behavior ("want
   total 9000, got ErrNotImplemented") or the stub's own error thrown from the
   call under test ("Error: not implemented" at `applyCoupon`), and you write
   it as the expected red in the test plan. Any other error is not red: fix
   the test or the stub. A test that passes against the stub does not test
   the new behavior, or the stub has logic.
4. Run the existing tests of the package or area again. A new test that breaks
   an old one means the spec changes existing behavior: that goes back to the
   spec as a question, it is not resolved in silence.

## Expected values come from the spec

- Write each expected value from the criterion or the key examples, as a
  literal: `Cart{Total: 90_00, Discount: 10_00}`, not a value computed with
  the same formula production uses. A test that recomputes the expected value
  passes with any bug in the formula.
- Never run the current code to copy what it returns, except in
  characterization tests. Models tend to write oracles that capture what the
  code does instead of what it should do.
- Assert on what the caller or the user observes: the return value, the HTTP
  status and body, the row in the database, the emitted event. Not on private
  calls, method order or call counts, unless the contract is the call ("an
  email is sent"), and even then prefer a fake that records what was sent.

### Assertions that discriminate

- The full expected value, not a trait of it: `assert.Equal(t, want, got)`,
  not `assert.NotNil(t, got)`.
- One behavior per test, named `<situation>_<expected result>`:
  `TestApplyCoupon_ExpiredCoupon_ReturnsErrExpired`. A test with two Act
  blocks is two tests.
- Forbidden: a test with no assertion, `assert true`, an assertion only on
  not-null or defined, a mock compared with itself, production logic copied
  into the test.

## Mocks only at boundaries

| Dependency | Use |
|---|---|
| The repo's own modules | the real implementation |
| The project's database | a real one (the test database or container the repo already uses) |
| Third-party HTTP | a fake server (`httptest.NewServer`, `msw`, `responses`, WireMock) with responses from the provider's docs |
| Clock and randomness | injected |

Never mock the unit under test. Each mock gets a one-line comment that says
why it is a boundary. Agents add mocks far more often than people do; the
auditor counts them.

## Pick the level by risk

Pure logic in a unit test; behavior that crosses modules or the database in an
integration test with real dependencies; a user flow in an end-to-end test with
a browser. Do not follow a quota. In a web feature, most new tests usually
belong in integration. With UI at yes and an end-to-end setup already in the
repo (a Playwright or Cypress config, an `e2e/` suite), an end-to-end test
covers the happy path of each user-facing criterion. Without one, do not add a
framework, browsers or a new dependency for it: the TC-n cases cover those
criteria, agentic testing runs them, and the test plan says so under "Not
tested". Exploratory testing is agentic testing's job either way.

When a criterion says "any", "always", "never", or describes a reversible
transformation, add a property-based test next to the examples:

| Pattern | Signal | Property |
|---|---|---|
| Round trip | export and import, serialize, encode | `decode(encode(x)) == x` |
| Invariant | "the total is never negative" | holds for every valid input |
| Idempotence | "retrying does not duplicate" | `f(f(x)) == f(x)` |
| Reference | a simple or old version exists | `new(x) == reference(x)` |
| Metamorphic | "filtering more never returns more" | `len(f(x, a and b)) <= len(f(x, a))` |

Libraries: Go `pgregory.net/rapid` (`-rapid.checks=1000`) or native fuzzing,
TS/JS `fast-check` (`numRuns: 1000`), Python Hypothesis
(`@settings(max_examples=1000)`), Java jqwik (`@Property(tries = 1000)`), Rust
proptest (`PROPTEST_CASES=1000`). Use what the repo already depends on before
adding one. Each counterexample a property finds becomes a permanent example
test, after you check it against the spec: about half of what an unfiltered
property run reports is noise.

When the task changes an API another service consumes, and the repo already
has contract tests (Pact or similar), update the consumer side.

## Characterization (refactor)

Fix the current behavior before anyone touches it, even when it looks odd.
Run the code in a test, assert a value you know is wrong, and use the real
value from the failure as the expected one. For large outputs use the
snapshot tool the repo already has: golden files in `testdata/` with an
`-update` flag (Go), `toMatchSnapshot()` (Jest, Vitest), `syrupy` or
`approvaltests` (Python), ApprovalTests (Java), `insta` (Rust). If the current
behavior is a bug, write it down as a finding and do not "fix" it in the test.

Then run mutation on the files the refactor will touch, so a hole in the net
shows up now and not after the change:

- Go: `gremlins unleash --output gremlins.json ./internal/cart/`
- TS/JS: `npx stryker run --mutate "src/cart/discount.ts"`
- Python: `mutmut run` with `paths_to_mutate` set to the files
- Java: `mvn org.pitest:pitest-maven:mutationCoverage -DtargetClasses='com.acme.cart.*'`
- Rust: `cargo mutants --file src/cart.rs`

For each survivor: equivalent (no observable change, say why in one line),
in arid code such as logging (exclude it in the tool's config), or a missing
test (write it, citing the criterion). If the tool is not installed and cannot
be installed in the sandbox, say so in the report.

## No flaky tests

Run the new tests 10 times in random order before you finish:

- Go: `go test ./internal/cart/... -run 'TestApplyCoupon_' -count=10 -shuffle=on`
- Jest: `for i in $(seq 10); do npx jest --randomize src/cart || exit 1; done`
- Vitest: `for i in $(seq 10); do npx vitest run --sequence.shuffle src/cart || exit 1; done`
- Playwright: `npx playwright test --repeat-each=10 --retries=0`
- Python: `pytest -p randomly --count=10 tests/test_cart.py` (pytest-randomly and pytest-repeat)
- Rust: `for i in $(seq 10); do cargo test -q apply_coupon_ || exit 1; done`

Against a stub, a red test fails the same way every time; a test that fails
differently between runs is flaky. Fix it or delete it. No test depends on
the real clock, the public network, execution order, data another test left,
or a fixed `sleep`: inject the clock and the seed, and wait for conditions.
No retries (`--reruns`, `retryTimes`) to make a test pass.

## Format before they lock

Thulu locks your files as you leave them. A locked test the project's
formatter rejects fails the `lint` gate in Verification, and Development
cannot fix it without opening an approval for a person: on one real task that
was a stop for five specs that only needed prettier.

Before you finish, on every test, fixture, helper and stub file you added
or changed (`git diff --name-only <base>...HEAD` plus untracked files):

1. Run the project's formatter in write mode on those files only, with the
   command the repo declares (`npm run format`, `npx prettier --write
   <files>`, `gofmt -w`, `black`, `ruff format`, `cargo fmt`).
2. Run its format check (`prettier --check`, `gofmt -l`, `ruff format
   --check`) and its linter on the same files. Fix what they report in your
   files; do not touch other files.
3. Run the new tests once more: formatting must not change a result.

Record each command and its exit code in the report. A project with no
formatter or linter declared: say so, and nothing else is needed.

## TC-n cases

TC-n are for people and for agentic testing: write one per criterion that
someone can observe in the product (a screen or an API response), following
`octopus-test-cases`. Put the AC-n it proves in `covers` and what must be in
place in `preconditions`, with literal data; neither goes in the
description. Steps are short, one action each; `expected` is the "Pass if"
line, observable and with real values. A case over about 12 steps mixes two
behaviors: split it.

## User tasks (UI only)

When the spec has UI at yes, write the user tasks for agentic testing's blind
pass in a `user-tasks` section (title "User tasks"). The blind tester gets the
persona, goal, data and success line; the reference optimum is for the
evaluator only.

```
U-1  Persona: shop owner who uses the app once a week, from a phone.
Goal: "You got a discount code by email. Buy the 'Black mug' using it."
Data: code PROMO10 (valid).
Success: the order is confirmed with 10% off.
Reference optimum: 5 actions.
```

- The goal is in the user's words, realistic and actionable ("do it", not
  "tell me where you would click").
- No term that appears as a label in the UI, no button, menu or route names,
  no order of steps. If you cannot phrase it without naming the UI, pick
  another task.
- One task, one success criterion. "Find the address and the holiday hours"
  is two tasks.

## locked-tests.txt

Thulu locks the test files this stage adds or modifies that match these
patterns, and only these: `*_test.go`, `*.test.ts`, `*.test.tsx`,
`*.test.js`, `*.test.jsx`, `*.spec.*`, `test_*.py`, `*_test.py`,
`*Test.java`, and any file under a `tests/`, `__tests__/` or `src/test/`
directory. Anything else your tests depend on goes in `locked-tests.txt` in
the output dir, one path per line, relative to the repo root: fixtures,
golden files, snapshots, `testdata/`, a helper outside those paths, a test
named `*.test.mjs` or `*.test.mts`, a suite under `test/` or `e2e/` whose
files are not `*.spec.*`:

```
internal/cart/testdata/coupons.golden
web/e2e/fixtures/checkout.json
```

A directory (`internal/cart/testdata/`) locks every file it holds when the
stage closes, including files that were there before the task. A file added to
it later is not locked: list the directory again, or the file, if you add one.
Never list a stub, or a directory that holds one: development has to replace
it.

## The test plan section

Write it with `task_section_set(slug="test-plan", title="Test plan")`.
Development, verification and review read it to know what is locked and how to
run it.

```
Kind: feature (new coupon rule; tests start red by assertion)
Base: 9ae7f70b (origin/main)
Run: go test ./internal/cart/ -run 'TestApplyCoupon_' -count=1

| AC | Test | Level | Expected red |
|---|---|---|---|
| AC-1 | internal/cart/discount_test.go TestApplyCoupon_ValidCode_TakesTenPercent | unit | want Total 9000, got ErrNotImplemented |
| AC-2 | internal/cart/discount_test.go TestApplyCoupon_ExpiredCoupon_ReturnsErrExpired | unit | want ErrCouponExpired, got ErrNotImplemented |
| AC-3 | TC-1 | manual | - |

Stubs: internal/cart/discount.go ApplyCoupon (thulu:stub)
Locked beyond the patterns: internal/cart/testdata/coupons.golden
Not tested: AC-5, depends on the payment provider; covered by TC-4
```

## What you do not do

- Implement behavior, refactor or reformat production code. The only
  production change is a stub, and you format it like your tests.
- Mark tests `skip`, `only`, `xit`, `t.Skip`, `@Disabled`, `#[ignore]`.
- Pick an interpretation when two criteria contradict each other, or when the
  Interface does not name what a test needs: ask with `task_ask`, quoting
  both.
- Report "tests fail as expected" without the output that shows it.
