\\ Fixture for the stage-3 computed-name hypothesis (docs/analysis-rules.md).
\\ The shake's soundness argument is that every name the artifact can call
\\ occurs syntactically in the artifact.  (intern "reverse") builds a callable
\\ name out of a string, so this program is outside that argument.  It is not
\\ an eval entry point and the shake does not refuse it: it prints
\\   yggdrasil-shake: WARN computed-name in computed-call
\\ and records computed-names= in both manifests.
(define computed-call
  Name -> (intern Name))

(output "interned ~A~%" (computed-call "reverse"))
