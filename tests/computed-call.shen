\\ Fixture for the containment check that can fail (docs/analysis-rules.md,
\\ `yggdrasil trace-check --full`).
\\
\\ shen.printF is a kernel defun that nothing in this program mentions: the
\\ name is assembled out of a string at run time and resolved through the
\\ kernel's own lambda table, so it is outside the shake's soundness argument
\\ -- every name the artifact can call occurs syntactically in it -- and
\\ outside `reach`.
\\
\\ The shaken slice cannot demonstrate this and that is the point.  It does
\\ not contain shen.printF, and trim-top restricts the lambda-table literal
\\ to the footprint, so (fn (intern "shen.printF")) there is an error rather
\\ than a trace record: called ⊆ reach holds on the slice by construction.
\\ The FULL artifact holds every kernel defun and the untrimmed table, so the
\\ call resolves, is recorded in the program phase, and the check catches it.
(define call-by-name
  Name X -> (let F (fn (intern Name)) (F X)))

(output "hidden = ~A~%" (call-by-name "shen.printF" (@p 7 8)))
