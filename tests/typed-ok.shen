\\ typed-ok: well-typed, eval-free fixture for the typecheck gate.
\\ Exercises both signature styles: an inline { ... } signature (survives
\\ any shake untouched) and a separate (declare ...) (stripped from
\\ eval-free shakes; folded into the define by the check's preprocessor).

(define double
  { number --> number }
  X -> (* 2 X))

(declare shout (string --> string))
(define shout
  S -> (cn S "!"))

(pr (shout (str (double 21))) (stoutput))
