# JSON Secondary Index / Search Design

Status: design target for the first SnugKV search milestone.

## Goal

Add opt-in secondary indexing for first-class JSON documents without increasing
the memory footprint of ordinary non-indexed SnugKV workloads.

The compatibility direction is Redis Search / RediSearch syntax where practical,
starting with a deliberately small, auditable subset instead of claiming full
RediSearch compatibility.

The first implementation target is:

- explicit indexes created with `FT.CREATE ... ON JSON`;
- JSON key-prefix filtering;
- scalar JSONPath fields declared in a schema;
- `TAG`, `NUMERIC`, and basic `TEXT` fields;
- `FT.SEARCH` for match-all, tag equality, numeric ranges, and field-scoped text tokens;
- deterministic result ordering;
- synchronous index maintenance for JSON writes/deletes;
- index rebuild from the primary dataset after restart.

Advanced full-text features such as stemming, scoring, phrases, fuzzy matching,
plus vector search, GEO fields and broader RediSearch query grammar are later phases.

## Non-goals for v1

The first version will not implement:

- general full-text search;
- fuzzy matching;
- stemming, stopwords or phonetics;
- BM25/scoring;
- vector indexes;
- aggregations;
- distributed indexes;
- persistence of derived posting lists;
- indexing arbitrary STRING/HASH keys;
- complete Redis Search command or error compatibility.

## Command surface

### FT.CREATE

Initial grammar:

```text
FT.CREATE index
  ON JSON
  [PREFIX count prefix [prefix ...]]
  SCHEMA
    path AS alias TAG
    path AS alias NUMERIC
    path AS alias TEXT
    ...
```

Initial constraints:

- `ON JSON` is required.
- `PREFIX` defaults to one empty prefix, meaning every JSON key.
- `AS alias` is required in the first implementation.
- Schema paths must be valid JSONPath expressions.
- TAG/NUMERIC index scalar results.
- TEXT indexes string values into normalized tokens.
- A schema path producing multiple scalar matches indexes all distinct values for
  that document.
- Duplicate index names are rejected.
- Duplicate aliases within an index are rejected.

Examples:

```text
FT.CREATE products ON JSON PREFIX 1 product: SCHEMA   $.category AS category TAG   $.price AS price NUMERIC

FT.CREATE users ON JSON PREFIX 1 user: SCHEMA   $.country AS country TAG   $.age AS age NUMERIC
```

### FT.SEARCH

Initial query subset:

```text
FT.SEARCH index "*"
FT.SEARCH index "@category:{books}"
FT.SEARCH index "@price:[10 50]"
FT.SEARCH index "@title:memory"
FT.SEARCH index "@category:{books} @price:[10 50]"
FT.SEARCH index "(@category:{books}) | (@category:{games})"
FT.SEARCH index "@category:{books} -@price:[50 +inf]"
FT.SEARCH index "-@category:{games}"
```

Space-separated expressions are implicit AND, pipe (`|`) is OR, and leading
dash (`-`) is unary NOT. Nested boolean queries use `DIALECT 2`, matching
Redis's modern precedence rules: `NOT > AND > OR`. Parentheses can override
precedence and may be nested. Redis 8 still defaults to DIALECT 1, so callers
that depend on the AST semantics should pass `DIALECT 2` explicitly. The
implemented predicate leaves include TAG equality, NUMERIC ranges, and
field-scoped TEXT token matching.

### TEXT v1

The first TEXT implementation is intentionally small:

- field-scoped terms such as `@title:memory`;
- grouped same-field multi-term AND queries such as `@title:(memory engine)`;
- trailing-wildcard TEXT prefix queries such as `@title:mem*` and `@title:(mem* eng*)`;
- exact adjacent TEXT phrase queries such as `@title:"memory guide"`;
- default English stemming for ordinary TEXT terms and phrases;
- per-field `NOSTEM` to disable stem postings;
- Redis-compatible default stopword filtering, `STOPWORDS 0`, and custom stopword lists;
- string JSON values only;
- case-insensitive token lookup;
- whitespace and punctuation separate tokens;
- underscore remains part of a token;
- exact token postings only.

Not yet implemented: unqualified full-text terms,
phonetics, suffix/infix wildcard expansion,
relevance scoring or broader language-specific tokenization beyond the audited English/German subset.

Wildcard execution supports three measured shapes over indexed surface terms: prefix (`mem*`), suffix (`*ory`), and contains (`*mor*`). Arbitrary internal globbing is intentionally not generalized because the audited Redis forms such as `m*mory` return zero matches rather than behaving like a generic glob engine.

Unqualified TEXT terms are evaluated across all indexed TEXT fields. Each unqualified clause unions matches across eligible TEXT fields, while the query AST preserves implicit AND/OR/NOT composition; quoted phrases must match within a single TEXT field.

Schema modifiers now include audited Redis-compatible `WEIGHT`, `SORTABLE`, and `NOINDEX`. `WEIGHT` is retained for future scoring, `NOINDEX` suppresses posting construction, and `SORTABLE` is represented in the schema without making sorting correctness depend on precomputed sortable storage.

TEXT prefix search currently supports only a single trailing `*`. Bare `*`,
leading wildcards, infix wildcards, and multiple `*` characters are rejected.

Exact phrase search uses `@field:"phrase"` and requires the normalized tokens to
appear adjacent and in order within the same indexed JSON string value. Grouped
TEXT queries support audited `SLOP` and `INORDER` options: proximity is applied to
grouped terms, while quoted exact phrases remain exact under those options. Fuzzy
TEXT queries use `%term%`, `%%term%%`, or `%%%term%%%`; edit distance is evaluated
against indexed surface terms, with exact stem-token hits also included. With the
default TEXT behavior, phrase tokens are compared through the English stemmer;
`TEXT NOSTEM` keeps exact normalized tokens instead. SnugKV does not yet expose
Redis phrase `SLOP`/`INORDER` controls.

The first language milestone supports Redis-compatible `LANGUAGE english|german`,
query `LANGUAGE english|german`, and JSON `LANGUAGE_FIELD`. English uses the
existing stemmer. German stemming is currently limited to the behavior directly
audited against Redis and should be expanded only with additional differential cases.

Stopwords are index-wide. Omitting `STOPWORDS` uses Redis's 33-word default list.
`STOPWORDS 0` disables filtering completely, and `STOPWORDS N ...` replaces the
default list with exactly the supplied words. Stopwords are removed both when
building TEXT postings/token sequences and when evaluating TEXT query terms.

For multi-term field scoping, SnugKV currently requires the explicit grouped
form `@field:(term1 term2)`. This matches Redis's unambiguous same-field
syntax across dialects and avoids adopting DIALECT 1's broader unparenthesized
field-modifier behavior before it is separately audited. Those remain explicit
compatibility boundaries until individually audited against Redis.

Initial options:

```text
LIMIT offset count
NOCONTENT
RETURN count path [AS alias] ...
SORTBY attribute [ASC | DESC]
```

`SORTBY` is applied before `LIMIT`. The first SnugKV implementation supports
indexed scalar TAG and NUMERIC attributes. It does not require the schema field
to be declared `SORTABLE`; Redis likewise permits sorting non-SORTABLE fields,
with `SORTABLE` acting as a latency/memory optimization rather than a
correctness requirement.

For equal sort values, SnugKV uses ascending binary key order as a deterministic
tie-breaker. Redis Search does not expose a stable ordering contract for equal
sort values, so tied documents can appear in a different order even when the
primary SORTBY ordering is equivalent. Callers that paginate across tied values
should not depend on Redis's incidental tie order.

The first implementation may land `LIMIT` and `NOCONTENT` before `RETURN`,
but result ordering and pagination semantics must be fixed before release.

### FT.DROPINDEX

Initial grammar:

```text
FT.DROPINDEX index
```

Dropping an index removes only derived index state. It never deletes JSON keys.

### FT._LIST

Expose currently defined indexes:

```text
FT._LIST
```

This is useful for tooling and restart/rebuild verification.


### FT.INFO

Expose the implemented index definition and live document count:

```text
FT.INFO index
```

The first subset reports the index name, JSON definition, prefixes, schema
attributes, indexed document count, and synchronous indexing status. It does not
invent RediSearch statistics for features SnugKV does not implement, such as
TEXT term counts, scoring records, or tokenizer memory.

## Result ordering

Redis Search does not promise a simple lexical key order for unsorted searches,
but SnugKV needs deterministic tests and reproducible behavior.

For the initial subset, results are returned in ascending binary key order unless
an explicit future `SORTBY` is supplied.

Live Redis 8.10 Search differential testing confirmed that unsorted result order
differs from SnugKV's deterministic ordering. Redis returned its internal index
order, while SnugKV returned ascending binary key order. Because Redis Search does
not provide a stable unsorted ordering contract, SnugKV keeps deterministic order
as an intentional compatibility boundary. Consequently, `LIMIT` without an
explicit sort can select a different key even when the total result set is
identical.

All audited search predicates, result counts, mutation visibility, deletion,
expiry, and default JSON content shape matched Redis 8.10.

## Index catalog

Search state is opt-in and owned by a dedicated index manager attached to
`engine.Store`.

Conceptually:

```go
type SearchManager struct {
    mu      sync.RWMutex
    indexes map[string]*JSONIndex
}

type JSONIndex struct {
    Name     string
    Prefixes []string
    Fields   []IndexField

    // Per-document reverse state used to remove old postings cheaply.
    docs map[string]DocumentIndexState

    tags     map[string]*TagFieldIndex
    numerics map[string]*NumericFieldIndex
}
```

The manager must be lazily allocated. A store with zero search indexes should pay
approximately one nil pointer / zero-value field, not per-key metadata.

## Field representation

### TAG

A TAG field indexes canonical scalar string values.

Initial accepted JSON values:

- string;
- boolean, encoded as `true` / `false`;
- finite number, encoded using the same canonical number formatting used by the
  JSON layer.

Objects and arrays are ignored unless the JSONPath expands them into scalar
matches.

Conceptual structure:

```go
map[value]postingSet
```

The posting set should start as a compact sorted key slice for small cardinality.
Do not commit to a bitmap implementation until measurements justify it.

### NUMERIC

A NUMERIC field accepts finite JSON numbers.

The first implementation should optimize correctness and predictable memory use
before introducing a complex tree.

Recommended first structure:

- per-field `map[key]float64` for document values;
- a lazily rebuilt sorted `[]numericPosting` generation for range scans;
- invalidate the sorted generation on mutation;
- rebuild it on the next numeric query.

This avoids bringing a B-tree dependency into the core before benchmarks show a
need.

If numeric-query write rates make rebuilds too expensive, replace this with an
ordered tree later without changing command semantics.

## Reverse document state

Every indexed document must retain the values it contributed to each index field.

Example:

```go
type DocumentIndexState struct {
    Fields map[string][]CanonicalIndexValue
}
```

This allows JSON mutation to remove the old postings without searching every
posting list.

Reverse state exists only for documents matching at least one active index.

## Mutation integration

Index maintenance must be synchronous with successful JSON mutations.

Affected operations include:

- `JSON.SET`;
- `JSON.DEL` / `JSON.FORGET`;
- `JSON.NUMINCRBY`;
- `JSON.STRAPPEND`;
- `JSON.ARRAPPEND`;
- `JSON.ARRPOP`;
- `JSON.ARRINSERT`;
- `JSON.ARRTRIM`;
- `JSON.CLEAR`;
- `JSON.TOGGLE`;
- `JSON.MERGE`;
- `JSON.MSET`;
- whole-key `DEL`, `UNLINK`, expiry, overwrite, rename, restore and copy where
  they affect indexed JSON keys.

Do not add one search hook to every JSON command.

Instead, index maintenance should attach to the common primary-key publish/remove
boundary so every mutation path is covered centrally.

The update model is:

1. capture the old logical JSON value when the key is currently indexed;
2. apply the primary-store mutation;
3. derive the new index values from the resulting logical JSON;
4. replace the document's postings in each matching index.

If a primary mutation is rolled back, index state must also return to the
pre-mutation state.

Because indexes are derived and rebuildable, an unexpected index-maintenance
failure must never leave durable primary data corrupted. The implementation
should prefer rebuilding or marking an index dirty over rolling back a committed
AOF record solely because a derived structure failed.

## Expiration and deletion

Expired or deleted keys must disappear from search results.

The preferred implementation is eager removal through the same central remove
hook used by ordinary key deletion/expiration.

Queries must still validate candidate keys against the primary store so stale
postings cannot return an expired document if an edge path misses cleanup.

Stale postings discovered during a query may be pruned opportunistically.

## Rename / COPY / RESTORE

Derived state follows logical key identity:

- `RENAME`: remove source postings and index the destination under its new key.
- `COPY`: source remains indexed; destination is independently indexed if its
  key prefix matches.
- `RESTORE`: index the restored JSON value if it matches an index.
- overwrite by non-JSON value: remove any old JSON postings.

## Persistence

Posting lists are derived state and are not written to AOF or snapshots.

Index definitions are persisted in a small versioned `.search` sidecar adjacent
to the active AOF path, or the snapshot path when AOF is disabled. On startup the
primary dataset is recovered first, then search definitions are loaded and all
postings are rebuilt from live JSON.

`FT.CREATE` and `FT.DROPINDEX` do not emit primary-key AOF records. Their
durability is handled only by the definition sidecar, preventing metadata-only
operations from serializing the full keyspace.

Definition-sidecar writes are atomic. If a write fails after an in-memory
definition mutation, SnugKV restores the previous definition set and rebuilds
its derived postings before returning an error.

## Rebuild model

Rebuild is deterministic:

1. snapshot the active index definitions;
2. scan live keys across shards;
3. consider only `TypeJSON` values;
4. filter by configured prefixes;
5. evaluate schema JSONPaths;
6. populate per-field postings and reverse document state;
7. atomically publish the rebuilt generation.

The first implementation performs a synchronous rebuild while holding the
primary shard locks so the rebuilt generation has a single atomic publication
boundary. This is acceptable during startup and rare definition rollback, but it
is intentionally not the long-term online-rebuild design.

A later optimization may build a new generation incrementally in the background
and atomically swap it in after catching up concurrent mutations.

## Concurrency

Search index catalog access uses its own lock and must not become a global write
lock for unrelated KV operations.

Rules:

- no index: no search lock on ordinary data operations;
- indexed JSON mutation: update only indexes whose prefixes match the key;
- query: acquire a stable index generation or read lock, then release it before
  expensive document materialization when possible;
- never acquire shard locks in an order that can deadlock with the search
  manager.

Before implementation, lock ordering must be documented in code next to the
manager.

## Memory accounting

Search memory is first-class memory and must be visible.

Add search counters to `SNUG.STATS` / memory diagnostics:

- index count;
- indexed document count;
- tag posting bytes;
- numeric posting bytes;
- reverse-state bytes;
- total search/index accounted bytes.

Search allocations count toward `max_memory`.

Do not hide secondary-index memory outside SnugKV's accounting model.

## OOM behavior

`FT.CREATE` may fail if building the initial index cannot fit within
`max_memory`.

For an already-created index, a primary JSON mutation must not silently disappear
from search because index memory is exhausted.

The first implementation should choose one explicit policy and test it:

1. reserve index-growth memory before committing the primary mutation; or
2. mark the index unavailable/dirty and rebuild after memory becomes available.

Failing open with stale successful search results is not acceptable.

The preferred v1 policy is reservation before commit for predictable consistency.

## Query execution

### Candidate generation

- TAG equality: posting lookup.
- NUMERIC range: sorted numeric generation + binary search.
- AND: start with the smallest estimated candidate set and intersect.

The query planner should remain intentionally simple in v1.

### Materialization

Unless `NOCONTENT` is supplied, fetch matching live JSON documents from the
primary store only after candidate selection.

Do not store duplicate full JSON documents in the index.

## JSONPath extraction

Index schema evaluation reuses `internal/jsonvalue`.

No second JSONPath implementation is allowed.

Schema extraction must use the same path parsing/matching semantics as JSON
commands so index results cannot disagree with `JSON.GET` for the same path.

## ACL

Search commands require explicit ACL metadata from the start.

Compatibility direction:

- `FT.SEARCH` is read;
- `FT.CREATE` and `FT.DROPINDEX` are write/admin-like search operations;
- all are members of the Redis `@search` category where the reference exposes
  them.

Key-pattern ACL semantics for an index query are not equivalent to a fixed key
argument because the query discovers keys dynamically.

Before claiming Redis compatibility, live differential tests must establish how
Redis Search combines `FT.SEARCH` with key-pattern ACL rules. Until then, the
safe SnugKV default is to filter returned documents through the caller's key ACL.

## COMMAND metadata

Every implemented FT command must have:

- arity;
- command flags;
- ACL category membership;
- key-spec behavior;
- `COMMAND INFO`;
- `COMMAND DOCS`.

Index names are not Redis keys and must not be reported by `COMMAND GETKEYS`.

## Test plan

### Unit

- schema parser;
- prefix matching;
- TAG canonicalization;
- NUMERIC validation;
- posting add/remove/idempotency;
- reverse-state replacement;
- query parser;
- range boundary inclusivity;
- AND intersection;
- deterministic pagination.

### Engine

- create index over existing JSON;
- mutation updates postings;
- path removal removes postings;
- whole-key deletion;
- expiry;
- overwrite JSON with non-JSON;
- rename/copy/restore;
- multiple indexes on one document;
- multi-match JSONPath schema fields.

### Server

- FT.CREATE grammar/errors;
- FT.SEARCH reply shape;
- LIMIT/NOCONTENT;
- ACL categories and key filtering;
- COMMAND metadata.

### Durability

Once definitions are durable:

- restart restores definitions;
- index rebuild returns identical results;
- AOF rewrite does not serialize posting lists;
- corrupted index-definition metadata fails safely without corrupting primary
  data.

### Differential

Compare the supported subset against a Redis 8.10 + Search reference:

- FT.CREATE ON JSON;
- PREFIX;
- TAG exact matches;
- NUMERIC ranges;
- implicit AND;
- LIMIT;
- missing fields;
- multiple JSONPath matches;
- mutation visibility;
- deletion/expiry;
- errors and reply shape.

Document all boundaries rather than silently diverging.

## Delivery phases

### Phase 1 — index core

- index catalog;
- schema representation;
- prefix matching;
- TAG postings;
- NUMERIC values + lazy sorted range generation;
- reverse document state;
- explicit memory accounting;
- unit tests.

### Phase 2 — mutation integration

- centralized publish/remove hooks;
- create index over existing data;
- update/delete/expiry/overwrite/rename/copy/restore correctness;
- race and OOM coverage.

### Phase 3 — command surface

- `FT.CREATE`;
- `FT.DROPINDEX`;
- `FT._LIST`;
- `FT.SEARCH`;
- `LIMIT`, `NOCONTENT`, then `RETURN`;
- COMMAND and ACL metadata.

### Phase 4 — durability and differential audit

- persist definitions;
- rebuild after restart;
- Redis 8.10 + Search differential harness;
- documentation of compatibility boundaries.

### Later

Only after measured demand:

- richer TEXT semantics: stemming, phrases, scoring, fuzzy/prefix search;
- richer SORTBY optimization / sortable-value storage;
- broader Redis Search query grammar beyond TAG/NUMERIC predicates;
- aggregation;
- GEO;
- vector search;
- background rebuild generations;
- compressed/bitmap postings.
