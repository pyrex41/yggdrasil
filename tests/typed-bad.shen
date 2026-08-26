\\ typed-bad: the declare contradicts the define (declared number -> number,
\\ body concatenates a string).  The typecheck gate must FAIL on `bad`;
\\ a plain shake still succeeds today, which is exactly the gap the gate
\\ exists to close.

(declare bad (number --> number))
(define bad
  X -> (cn X "!"))
