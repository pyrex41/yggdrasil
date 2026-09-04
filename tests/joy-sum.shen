\\ Fixture: bounded first-order subset accepted by the shen-joy target.
(define sum-mid
  Acc 0 -> Acc
  Acc N -> (sum-mid (+ Acc N) (- N 1)))

(output "~A~%" (sum-mid 0 8000))
