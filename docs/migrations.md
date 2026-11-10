# Database migrations

Each store keeps its own list of migrations; `internal/schema` collects them
all for the checks below and for `go run ./cmd/migrations list -json`.

## The rule

**The binary of release N-1 works on the schema of release N.**

That makes rolling back from N to N-1 a change of images: the database stays
as it is. A change that would break the rule is spread over several releases.

Two more guarantees, both tested in CI:

- An upgrade can jump from any version in `upgrade_from` straight to N.
- A published migration never changes.

## How to write each kind of change

| Change | Releases | How |
|---|---|---|
| New table, or new column that is nullable or has a DEFAULT | 1 | directly |
| New `NOT NULL` column | 1 | always with a `DEFAULT`, so the old binary's INSERTs still pass |
| Drop a table or column | 2 | N: the code stops reading and writing it. N+1: `DROP` |
| Rename column `a` to `b` | 3 | N: add `b`, write both, read `b` falling back to `a`, and an idempotent backfill that runs again at every start. N+1: stop writing `a`. N+2: `DROP a` |
| Change a type | 3 | like a rename, with a new column of the new type |
| New constraint (UNIQUE, CHECK, FK, `SET NOT NULL`) | 2 | N: the code already obeys it and cleans old rows. N+1: add the constraint |
| Rewrite data (a bulk UPDATE to a new format) | 2 | N: the code reads both formats. N+1: rewrite |

The rename's backfill runs at every start to cover a rollback: back on N-1,
that binary writes only `a`, and on the way forward again the copy has to
happen once more.

A destructive step carries a `Contract` naming the release in which the code
stopped using what it drops or changes:

```go
migrate.Migration{ID: 31, SQL: "ALTER TABLE tasks DROP COLUMN legacy_state",
	Contract: &migrate.Contract{StoppedUsingIn: "1.1.0", Reason: "read from state since 1.1.0"}}
```

Some breaks have no destructive DDL at all, such as a column with `DEFAULT ''`
whose empty value an older binary would write where the new code expects a
hash, or a new value in a text column that older code cannot read. No static
check sees those; the `compat` gate does, as long as its smoke test exercises
that part.

## The gates

All three run in CI and in `make check-ci`.

**`migrations-check` (immutability).** `internal/schema/migrations.lock` lists
every migration with its component, ID, the sha256 of its SQL and the release
it shipped in (`released_in`, empty until then). A published entry must keep
its hash, IDs grow within a component, and no migration in the code may be
missing from the lock. `make release` fills `released_in`; `make
migrations-lock` regenerates the unpublished entries. Releases before 1.0.0
were private previews: the lock still names them, and every migration they
shipped runs on a fresh install.

**`migrations-lint` (static).** Every statement of an unpublished migration is
classified: `DROP TABLE`, `DROP COLUMN`, `RENAME`, `ALTER ... TYPE`, `SET NOT
NULL`, `ADD COLUMN ... NOT NULL` without `DEFAULT`, `ADD CONSTRAINT`, `CREATE
UNIQUE INDEX` on an existing table, `UPDATE` and `DELETE`. Any of them fails
unless the migration has a `Contract` whose `StoppedUsingIn` is a release the
lock already marks as published. A table created by an unpublished migration
is not existing: the previous release never saw it, so its constraints and
indexes do not count. A semgrep rule also refuses `SELECT *`, `RETURNING *`
and `INSERT INTO t VALUES` without a column list, because the old binary's
`Scan` would fail once a column is added.

**`compat` (dynamic).** Postgres and two daemons, the last release and the
current commit, take turns on one database: the release seeds data, the
commit migrates and writes, the release runs again on the migrated schema and
must read everything (the audit chain included), and the commit runs once
more. Any 5xx, SQL error in the logs or failed assertion fails it. A second
case jumps from the oldest version in `upgrade_from` straight to the commit.
The smoke test lives in `test/compat/`; run it locally with `make compat`.
