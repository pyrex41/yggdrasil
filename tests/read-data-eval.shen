\\ Fixture: the falsifying half of tests/read-data.shen (#27).  The same
\\ declaration, but the program evaluates what it reads, so the read value
\\ reaches `eval` and the shake must stay eval-capable: needs-eval=true, no
\\ read-data= key, the full reader and compiler kept.  The declaration is
\\ inert here, and a shake that honoured it would be unsound.

(set yggdrasil.*read-data* true)

(output "eval of read: ~A~%" (eval (hd (read-from-string "(* 6 7)"))))
