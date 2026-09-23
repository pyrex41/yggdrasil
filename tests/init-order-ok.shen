\\ Fixture for the stage-2 initialisation-order check (docs/analysis-rules.md):
\\ the same two reads as tests/init-order-bad.shen, in an order that boots.
\\ Every (value ...) in a toplevel form is preceded by the (set ...) that
\\ writes it, so the shake accepts the program and both manifests carry
\\ init-order=checked.  The read inside f does not count: it runs when f is
\\ called, not while the artifact initialises.

(set *greeting* "hello from an initialised global")

(define f
  X -> (@s (value *greeting*) X))

(output "~A~%" (f " (via f)"))
(output "~A~%" (value *greeting*))
