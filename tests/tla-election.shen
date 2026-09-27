\\ A realistic user program with a known answer: the tla.shen model checker
\\ (tests/lib/tla.shen) running the leader-election spec from its examples
\\ (tests/lib/election.shen).  About 100 user functions, closures,
\\ absvectors, (fn F) on symbols, higher-order use throughout.
\\
\\ It is also the multi-file fixture.  The two leading loads below are
\\ hoisted by the shake (yggdrasil.shen, "multi-file programs"): each file
\\ becomes a user= entry of its own, in load order, and `load` never reaches
\\ the KL, so the program stays eval-free.  Relative paths resolve against
\\ this file's directory.
\\
\\ Three checks, each deterministic (breadth-first, successors in the
\\ spec's own order), so the transcript is a golden:
\\   1. safety only: every reachable state has at most one leader - OK;
\\   2. liveness under WF(Next): without timeouts a split vote deadlocks,
\\      so <>has-leader fails with a four-state counterexample;
\\   3. the double-vote bug: a computer may vote twice, and two leaders
\\      appear - the shortest invariant violation.
\\
\\ The three run inside ONE toplevel form.  The kl target's stdout is a REPL
\\ transcript that echoes each toplevel form's value, so output split over
\\ several forms arrives interleaved with echoes and fails containment of
\\ the golden (see metaeval:kl in scripts/parity-gate.sh).

(load "lib/tla.shen")
(load "lib/election.shen")

(do (tla.report
      (tla.check (election.init) (fn election.next)
                 [[deadlock false]
                  [invariant one-leader (fn election.one-leader?)]]))
    (tla.report
      (tla.check (election.init) (fn election.next)
                 [[deadlock false]
                  [invariant one-leader (fn election.one-leader?)]
                  [eventually has-leader (fn election.has-leader?)]
                  [sf voting (fn election.a-vote?)]]))
    (set *double-vote* true)
    (tla.report
      (tla.check (election.init) (fn election.next)
                 [[deadlock false]
                  [invariant one-leader (fn election.one-leader?)]])))
