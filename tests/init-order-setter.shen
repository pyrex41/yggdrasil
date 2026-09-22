\\ Fixture for the stage-2 initialisation-order check (docs/analysis-rules.md).
\\ The global *cfg* is never set by a toplevel (set *cfg* _); it is set by a
\\ function that a toplevel form APPLIES.  That is a perfectly ordered
\\ initialisation - setup runs before the read, and there is nothing here to
\\ reorder - so the shake must accept it.
\\
\\ The check discharges the read through the call graph (writesVia), which is
\\ an over-approximation of what a call does, so both manifests record
\\ init-order=checked-weak rather than =checked.
\\
\\ There is deliberately no tests/init-order-setter.expected: the parity gate
\\ only runs fixtures with a committed golden beside them.  Running it prints
\\ 42.

(define setup -> (set *cfg* 41))

(setup)

(print (+ (value *cfg*) 1))
