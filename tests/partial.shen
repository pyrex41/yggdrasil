\\ Fixture: Tarver's "code creep" example from the Shen group (The Future
\\ of Shen, 2026-09).  f is partial, so its compiled KL ends in
\\ (shen.f-error f) at the REPL - but the S42 kernel's own bootstrap
\\ rewrites that to a plain simple-error (shen.partial, load.kl), and the
\\ shake does the same to the kernel's internal shen.f-error callers when
\\ the program is eval-free, so the tracker never enters the footprint.
\\ Compare tests/partial-eval.shen.
(define f
  {number --> number}
  0 -> 1)

(output "f 0 = ~A~%" (f 0))
