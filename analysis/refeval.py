#!/usr/bin/env python3
"""Reference evaluator for analysis/analysis.dl.

    python3 analysis/refeval.py FACTSDIR [RELATION]

prints the named output relation, sorted, one tuple per line (tab-separated
for arity > 1); RELATION defaults to `reach`.  FACTSDIR is a directory
written by `yggdrasil facts PROG FACTSDIR`.

Why this exists: Soufflé is the oracle CI runs, but it is not installable
everywhere Yggdrasil is developed, and stage 1's whole claim is that the
rules describe the shake -- a claim nobody can check locally if checking it
needs a toolchain.  So the same rules get a second, independent
implementation in stdlib Python: bottom-up semi-naive evaluation over sets of
tuples, ~100 lines, no dependencies.  Disagreement between this and Soufflé
is a bug in one of them; the Go oracle test runs Soufflé when it is on PATH
and this otherwise, and CI runs Soufflé.

The rules are transcribed from analysis.dl clause for clause, in the same
order, with the same deviation numbering (D1-D8) -- read that file first.
Stratification is by hand rather than computed: the only negation is
`evalfree :- !anyeval`, and `anyeval` is derived from facts alone, so
evaluating the mode first and everything else after is a valid stratum
order.
"""

import os
import sys

# Relations read from FACTSDIR, with their arity. A missing file is an empty
# relation, not an error: the dump writes all twenty-one, but a hand-built fact
# directory that omits one should still evaluate.
INPUTS = {
    "kernel": 1,
    "callpos": 2,
    "argpos": 3,
    "datasym": 2,
    "mentionsprim": 2,
    "top": 2,
    "formmentions": 2,
    "formmentionsef": 2,
    "rawsym": 1,
    "usersym": 1,
    "entry": 1,
    "prim": 1,
    "cap": 2,
    "portGlobal": 1,
    "initprim": 1,
    "userintern": 1,
    "userglobal": 1,
    "initwrite": 1,
    "defunwrite": 1,
    # The runtime trace: empty in an ordinary dump, filled by
    # `yggdrasil trace-check`. readGlobal lives in readglobal.facts, the name
    # stage 4 consumes (docs/analysis-rules.md, "Runtime trace").
    "called": 1,
    "readGlobal": 1,
}

# Relations whose fact file is not named after the relation.
FILENAMES = {"readGlobal": "readglobal"}

F_ERROR = "shen.f-error"


def load(factsdir):
    """Read the TSV fact files into sets of tuples."""
    db = {}
    for name, arity in INPUTS.items():
        rows = set()
        path = os.path.join(factsdir, FILENAMES.get(name, name) + ".facts")
        try:
            handle = open(path, encoding="utf-8")
        except FileNotFoundError:
            db[name] = rows
            continue
        with handle:
            for line in handle:
                line = line.rstrip("\n").rstrip("\r")
                if not line:
                    continue
                cols = line.split("\t")
                if len(cols) != arity:
                    raise SystemExit(
                        "%s: expected %d columns, got %d: %r"
                        % (path, arity, len(cols), line)
                    )
                rows.add(tuple(cols))
        db[name] = rows
    return db


def closure(seed, successors):
    """Least fixpoint of X :- seed(X); X :- X(F), successors(F, X).

    Semi-naive: each round expands only the tuples discovered in the previous
    round, so every edge is followed once. This is the same least fixpoint the
    worklist `reach` in yggdrasil.shen computes, by the same argument that
    makes traversal order immaterial (docs/reachability.md).
    """
    out = set(seed)
    delta = set(seed)
    while delta:
        nxt = set()
        for f in delta:
            for g in successors.get(f, ()):
                if g not in out:
                    out.add(g)
                    nxt.add(g)
        delta = nxt
    return out


def evaluate(db):
    """Apply analysis.dl's rules; returns {relation: set of tuples}."""
    one = lambda rel: {t[0] for t in db[rel]}

    kernel = one("kernel")
    entry = one("entry")
    prim = one("prim")
    rawsym = one("rawsym")
    usersym = one("usersym")
    initprim = one("initprim")

    # ---- mode -------------------------------------------------------
    # evalcapable(S) :- rawsym(S), entry(S).
    evalcapable = rawsym & entry
    anyeval = bool(evalcapable)
    evalfree = not anyeval

    # ---- edges (D1, D3) ---------------------------------------------
    # callpos and argpos both yield edges; shen.f-error's whole row is
    # stripped when the program is eval-free.
    succ = {}
    def add_edge(f, g):
        if g in kernel:
            succ.setdefault(f, set()).add(g)

    for f, g in db["callpos"]:
        if f != F_ERROR or anyeval:
            add_edge(f, g)
    for f, g, _c in db["argpos"]:
        if f != F_ERROR or anyeval:
            add_edge(f, g)
    # D2: datasym derives no edge. Deliberately not iterated.

    # ---- seeds (D4, D5) ---------------------------------------------
    mentions = "formmentions" if anyeval else "formmentionsef"
    floorseed = {g for _n, g in db[mentions]} & kernel
    userseed = (rawsym if anyeval else usersym) & kernel
    seed = floorseed | userseed

    # ---- reach / floor (D6: kernel-restricted) ----------------------
    reach = closure(seed, succ)
    floor = closure(floorseed, succ)

    # ---- primitives and capabilities --------------------------------
    usedprim = set(initprim)
    for f, p in db["mentionsprim"]:
        if p not in prim or f not in reach:
            continue
        if f == F_ERROR and evalfree:
            continue
        usedprim.add(p)
    if F_ERROR in reach and evalfree:
        # rewrite-f-error's replacement body
        usedprim.update(("simple-error", "cn"))
    usedprim |= prim & (rawsym if anyeval else usersym)

    reaches = {c for c, p in db["cap"] if p in usedprim}

    # ---- runtime trace ----------------------------------------------
    # uncoveredCall(F) :- called(F), kernel(F), !reach(F).
    # uncoveredRead(V) :- readGlobal(V), !initwrite(V), !defunwrite(V),
    #                     !portGlobal(V).
    called = one("called")
    readglobal = one("readGlobal")
    uncoveredcall = (called & kernel) - reach
    written = one("initwrite") | one("defunwrite") | one("portGlobal")
    uncoveredread = readglobal - written

    # ---- computed names (stage 3, decides nothing) ------------------
    # computedName(F) :- userintern(F).  computedName(F) :- userglobal(F).
    computedname = one("userintern") | one("userglobal")

    return {
        "evalcapable": {(s,) for s in evalcapable},
        "reach": {(g,) for g in reach},
        "floor": {(g,) for g in floor},
        "usedprim": {(p,) for p in usedprim},
        "reaches": {(c,) for c in reaches},
        "needsEval": {("1",)} if "eval-kl" in usedprim else set(),
        "computedName": {(f,) for f in computedname},
        "called": {(f,) for f in called},
        "readGlobal": {(v,) for v in readglobal},
        "uncoveredCall": {(f,) for f in uncoveredcall},
        "uncoveredRead": {(v,) for v in uncoveredread},
    }


def main(argv):
    if len(argv) < 2:
        print(__doc__.strip().splitlines()[2].strip(), file=sys.stderr)
        return 2
    factsdir = argv[1]
    relation = argv[2] if len(argv) > 2 else "reach"
    out = evaluate(load(factsdir))
    if relation not in out:
        print(
            "refeval: no such output relation %r (have: %s)"
            % (relation, ", ".join(sorted(out))),
            file=sys.stderr,
        )
        return 2
    for tup in sorted(out[relation]):
        print("\t".join(tup))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
