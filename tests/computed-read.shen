\\ Fixture for stage 4's runtime half (docs/analysis-rules.md, `trace-check
\\ --prune-init`).  The name of the global is built out of a STRING at run
\\ time, so no symbol `shen.*tc*` occurs anywhere in the emitted KL: rawsym
\\ cannot keep it alive, no reachable defun has a readsIn row for it, no kept
\\ toplevel form reads it, and the go port does not declare it in port_reads.
\\ --prune-init therefore deletes its (set shen.*tc* false) from the
\\ initialiser -- and the artifact reads it anyway.
\\
\\ The program writes it first, so the RUN is correct and its stdout matches
\\ the golden; what is wrong is the artifact, which now reads a global nothing
\\ in its own initialisation establishes.  That is the whole point: a stdout
\\ comparison cannot see this, and the trace can.
(define global-of
  Name -> (intern Name))

(define poke
  Name V -> (set (global-of Name) V))

(define peek
  Name -> (value (global-of Name)))

(output "tc = ~A~%" (let W (poke "shen.*tc*" true) (peek "shen.*tc*")))
