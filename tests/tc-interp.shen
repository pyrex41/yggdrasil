\\ tc-interp: needs-eval typecheck fixture for option 5 rung 1 C.
\\ (tc +) and load are eval entry points, so the shake keeps types.kl
\\ (declare + typecheck tables) and t-star.kl in kernel.kl.
\\ Runtime load typechecks the L interpreter; then one normal-form.

(tc +)
(trap-error
  (do (load "interpreter.shen")
      (pr (cn "normal-form = " (cn (str (normal-form [[[y-combinator [/. ADD [/. X [/. Y [if [= X 0] Y [[ADD [-- X]] [++ Y]]]]]]] 3] 4])) "
")) (stoutput)))
  (/. E (pr (error-to-string E) (stoutput))))
(pr (cn "inferences = " (cn (str (inferences)) "
")) (stoutput))
