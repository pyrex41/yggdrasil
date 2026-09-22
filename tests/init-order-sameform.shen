\\ Fixture for the stage-2 initialisation-order check (docs/analysis-rules.md).
\\ Two more programs that boot correctly and that a strictly-earlier,
\\ freeze-blind write side refuses:
\\
\\   - the first form both sets *x* and reads it.  There is no EARLIER form
\\     to point at, and nothing to reorder: the write and the read are one
\\     toplevel form, and the write runs first.
\\   - *f* is set inside a freeze that the same form thaws.  ygg.io-writes
\\     stops at a freeze on purpose (stage 4 must not treat an unthawed
\\     freeze as an initialisation), so the write side needs its own deeper
\\     walk, `formwrite`, for this one.
\\
\\ Neither relaxation is precise - nothing here knows in which order a `do`
\\ runs, or whether a freeze is ever thawed - so both are on the weak side of
\\ the manifest key: this program records init-order=checked-weak.  What is
\\ still refused is a read with no write anywhere up to and including its own
\\ form, which is tests/init-order-bad.shen.
\\
\\ There is deliberately no tests/init-order-sameform.expected: the parity
\\ gate only runs fixtures with a committed golden beside them.  Running it
\\ prints 12.

(do (set *x* 1) (print (value *x*)))

(thaw (freeze (set *f* 2)))

(print (value *f*))
