\\ ShenOSKernel 43.1 slice oracle: these runtime observations require the
\\ kernel's public signature registry and macro context registration.  The
\\ shaker must retain them only for programs that mention the observations.

(define signature-visible?
  -> (if (get char-stream? shen.*sigf*) true false))

(define macro-context-visible?
  -> (macroexpand [shen.@c [shen.@ch foo]]))

(output "signature-visible: ~A~%" (signature-visible?))
(output "macro-context-visible: ~A~%" (macro-context-visible?))
