\\ Fixture: a program that reads S-expression DATA from stdin with `read`,
\\ and stays eval-free (#27).
\\
\\ `read` is an eval entry point, and on S42 rightly so: the kernel's reader
\\ can evaluate code by itself (a package form's exceptions, defmacro,
\\ synonyms, datatype).  The declaration below says this program's reads
\\ are data, and the shake takes the read-data mode: the reader and the
\\ macroexpander stay, the four paths from the reader to eval are replaced
\\ by named errors, and the manifest says needs-eval=false, read-data=true.
\\ See docs/eval-free-cli.md.
\\
\\ tests/read-data.stdin ends with the symbol `end`, so the loop never
\\ depends on how a port's reader reports end of stream.  Each form is
\\ printed as the kernel's reader returns it - macroexpanded, and with its
\\ applications curried by shen.process-applications - which is exactly
\\ what the read-data slice has to reproduce.  The numbers are then summed
\\ by pattern matching over the data, the eval-free half of the program.

(set yggdrasil.*read-data* true)

(define read-all
  S Acc -> (let F (read S)
             (if (= F end) (reverse Acc) (read-all S [F | Acc]))))

(define sum-numbers
  N -> N  where (number? N)
  [X | Y] -> (+ (sum-numbers X) (sum-numbers Y))
  _ -> 0)

(define show
  [] -> done
  [F | Fs] -> (do (output "~S~%" F) (show Fs)))

(let Forms (read-all (stinput) [])
  (do (show Forms)
      (output "forms: ~A~%" (length Forms))
      (output "sum: ~A~%" (sum-numbers Forms))
      (output "from string: ~S~%" (read-from-string "(port 80) [a b]"))))
