\\ Fixture: tests/partial.shen made eval-capable by one stray mention of
\\ eval, so nothing is stripped and shen.f-error keeps its real body -
\\ the interactive track-prompt, which calls read, which drags the
\\ reader, the typechecker and eval.  This is the creep Tarver described.
(define f
  {number --> number}
  0 -> 1)

(set *keep-eval* eval)
(output "f 0 = ~A~%" (f 0))
