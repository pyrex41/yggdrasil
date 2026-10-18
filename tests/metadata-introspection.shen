\\ ShenOSKernel 43.1 slice oracle: these runtime observations require the
\\ kernel's public signature registry and macro context registration.  The
\\ shaker must retain them only for programs that mention the observations.

(define signature-visible?
  -> (map (/. F [(element? F (external shen))
                 (not (empty? (assoc F (value shen.*sigf*))))])
           [char-stream? read-unit-string write-string]))

(define macro-context-visible?
  -> (macroexpand [shen.@c (cons a (cons (intern "bar!")
                                         (cons (protect Tail) [])))]))

(define macro-context-visible-h?
  -> (macroexpand [shen.@ch (cons a (cons (intern "bar!")
                                          (cons (protect Tail) [])))]))

(output "signature-visible: ~A~%" (signature-visible?))
(output "macro-context-visible: ~A~%" (macro-context-visible?))
(output "macro-context-visible-h: ~A~%" (macro-context-visible-h?))
