\\ Yggdrasil stage-2 Scheme (Chez) builder — the KL->Scheme half.
\\
\\ Run on shen-scheme AFTER loading its compiler + build helpers:
\\   shen-scheme eval -q -l src/compiler.shen -l scripts/build.shen \
\\                     -l <yggroot>/builders/scheme/build.shen \
\\                     -e "(_scm.initialize-compiler)" \
\\                     -e "(scm-rat-build \"<shaken-dir>\" [\"<user.kl>\" ...] \"<out-dir>\")"
\\
\\ Compiles the shaken kernel.kl + user .kl into Scheme with shen-scheme's own
\\ _scm.kl->scheme (the SAME compiler shen-scheme uses on the full kernel) and
\\ writes, into <out-dir>:
\\   kernel.scm  shaken kernel defuns, compiled
\\   user.scm    user defuns, compiled
\\   init.scm    kernel top-level (non-defun) forms ++ (shen.initialise)
\\               ++ user top-level forms, compiled — run after the defuns.
\\ build.sh assembles these with the shen-scheme runtime (chez-prelude +
\\ primitives + globals) into a self-contained app.scm run by `chez --script`.
\\
\\ No package: reuses the GLOBAL helpers from scripts/build.shen
\\ (sexp->string, defun?, build.filter, read-file-unprocessed, for-each).

(define scm-rat-emit
  S Out -> (pr (make-string "~A~%~%" (sexp->string S)) Out))

\\ Compile the defuns of From into To; return From's non-defun top-level forms.
\\ Exclude defuns shen-scheme overrides (pr, shen.char-stoutput?, dict ops, …):
\\ the port supplies its own via compiled/overrides.scm, exactly as shen-scheme's
\\ own build does. Requires (load-overrides) to have run first.
(define scm-rat-compile-file
  From To -> (scm-rat-compile-files [From] To))

\\ Several KL files into one .scm: every file's defuns, in order, and every
\\ file's toplevel forms, in order.  A multi-file program (one user= line per
\\ file, docs/shake.md) is compiled this way, so all of its defuns exist
\\ before the first toplevel form runs.
(define scm-rat-compile-files
  Froms To -> (let Kl   (mapcan (/. F (read-file-unprocessed F)) Froms)
                   Defs (build.filter (/. X (and (defun? X) (not (overidden? X)))) Kl)
                   Tops (build.filter (/. X (and (cons? X) (not (defun? X)))) Kl)
                   Out  (open To out)
                   W    (for-each (/. D (scm-rat-emit (_scm.kl->scheme D) Out)) Defs)
                   C    (close Out)
                   Tops))

(define scm-rat-build
  Shaken Users Out
  -> (let KTops (scm-rat-compile-file (cn Shaken "/kernel.kl") (cn Out "/kernel.scm"))
          UTops (scm-rat-compile-files (map (/. U (cn Shaken (cn "/" U))) Users)
                                       (cn Out "/user.scm"))
          \\ Defines-before-code: kernel tops (arity tables, *special*, ...),
          \\ then (shen.initialise), then the user program's top-level forms.
          Inits (append KTops (append [[shen.initialise]] UTops))
          IOut  (open (cn Out "/init.scm") out)
          W     (for-each (/. F (scm-rat-emit (_scm.kl->scheme F) IOut)) Inits)
          C     (close IOut)
          (output "scm.rat: wrote kernel.scm + user.scm + init.scm to ~A~%" Out)))
