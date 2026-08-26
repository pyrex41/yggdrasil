\\ typed-unsigned: a define with no inline signature and no declare.  The
\\ typecheck gate requires one or the other (mirroring the kernel's own
\\ "missing { in F" load-under-tc semantics) and must FAIL naming `nosig`.

(define nosig
  X -> X)
