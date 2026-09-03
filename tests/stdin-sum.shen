\\ Fixture: an eval-free program that consumes stdin.
\\
\\ The point is what it does NOT call.  (read (stinput)) reads an S-expression,
\\ which needs the kernel reader, and `read` is one of Yggdrasil's
\\ *eval-entry-points* -- so a program whose driver reads stdin that way is
\\ needs-eval=true however eval-free its own logic is, and the shake drags the
\\ reader/macroexpander/eval machinery back in.  Reading BYTES needs no reader:
\\ read-byte is a primitive.  A CLI-shaped program therefore stays in the
\\ eval-free regime by slurping bytes and parsing them itself.
\\
\\ The digest is position-sensitive (sum of byte * 1-based index) but uses only
\\ + and *: no division, no modulus, and no `floor` -- floor is not in the
\\ shaken footprint, and reaching for it here failed with "variable floor not
\\ bound" at run time rather than at shake time.  Values stay far inside exact
\\ integer range for fixture-sized input, so hosts with float64-only numbers
\\ agree with hosts that have a bignum tower.
\\
\\ Prints twice around a "===" line, so the parity gate's two-pass check
\\ applies (docs/parity.md).

(define slurp
  S Acc -> (let B (read-byte S)
                (if (= B -1) (reverse Acc) (slurp S [B | Acc]))))

(define digest
  [] _ H -> H
  [B | Bs] I H -> (digest Bs (+ I 1) (+ H (* B I))))

(define count-bytes
  [] N -> N
  [_ | Bs] N -> (count-bytes Bs (+ N 1)))

(define report
  Bs -> (do (output "bytes: ~A~%" (count-bytes Bs 0))
            (output "digest: ~A~%" (digest Bs 1 0))))

(let Bs (slurp (stinput) [])
  (do (report Bs)
      (output "===~%")
      (report Bs)))
