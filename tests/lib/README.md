# Library files for multi-file fixtures

Files here are loaded by fixtures in `tests/`, never shaken on their own:
the fixture globs are `tests/*.shen`, so nothing in this directory is
picked up as a fixture.

| file | from |
|---|---|
| `tla.shen` | pyrex41/shencheck `lib/tla/tla.shen` at `c706bc2` (branch `claude/tla-plus-shen-synthesis-wqw29z`), unmodified, BSD-3-Clause |
| `election.shen` | pyrex41/shencheck `lib/tla/examples/election.shen` at the same commit, unmodified |

`tests/tla-election.shen` loads both. Update them together, from one
shencheck commit, and regenerate `tests/tla-election.expected` from a run
rather than by hand.
