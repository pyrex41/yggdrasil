\\                            Yggdrasil - stage 2 builder (Common Lisp / SBCL)
\\
\\ Run via the shen-cl binary from the Yggdrasil directory, after loading
\\ yggdrasil.shen (for the ygg.* list helpers):
\\
\\   shen eval -q -l yggdrasil.shen -l builders/lisp/build.shen \
\\        -e '(lsp.build "out" "/abs/path/to/exe")'
\\
\\ build.sh wraps this and then runs sbcl on DIR/driver.lsp.
\\
\\ Reads DIR/yggdrasil.manifest - the sexp manifest, because read-file
\\ parses its paren rows directly into plain lists (the .txt manifest would
\\ need string parsing).  Then:
\\
\\   - compiles DIR/kernel.kl with shen-cl.kl->lisp -> DIR/kernel.lsp
\\   - compiles each user .kl -> DIR/<name>.lsp; toplevel non-defun forms
\\     are wrapped, in source order, into (DEFUN |yggdrasil.toplevel-<name>| () ...)
\\     which the driver's saved toplevel runs after (shen.initialise)
\\   - copies the shen-cl runtime sources (package.lsp primitives.lsp
\\     native.lsp shen-utils.lsp overwrite.lsp) and the static driver.lsp
\\     into DIR, so DIR is self-contained; eval-capable programs
\\     (needs-eval=true) additionally get shen-cl's precompiled KL->Lisp
\\     compiler (compiled/compiler.lsp) so eval-kl works at runtime
\\   - writes DIR/yggdrasil.config.lsp telling the driver which user modules
\\     to load and where save-lisp-and-die should write the executable
\\
\\ Strategy note: this reuses shen-cl's own runtime .lsp files rather than
\\ the 34.6-era Primitives/CL snippets (kept in the repo as historical
\\ reference only).

\\ The binary carries the KL->Lisp compiler (compiled/compiler.lsp) as Lisp
\\ functions, but they are absent from the Shen arity table, so eval cannot
\\ apply them until the arities are registered.
(shen.store-arity shen-cl.kl->lisp 1)
(shen.store-arity shen-cl.cl 1)

\\ Compiler flags, as shen-cl's boot would set them.  *compiling-shen-sources*
\\ enables trap-error optimisations that are only sound for kernel sources;
\\ lsp.compile-kernel turns it on, user code is compiled with it off.
(set shen-cl.*factorise-patterns* true)
(set shen-cl.*compiling-shen-sources* false)

(set lsp.*shen-cl* "../shen-cl/")
(set lsp.*driver* "builders/lisp/driver.lsp")
(set lsp.*runtime-files*
     ["package.lsp" "primitives.lsp" "native.lsp" "shen-utils.lsp" "overwrite.lsp"])

\\ Guard against the Shen printer truncating anything big (error messages
\\ aside, all printing below goes through lsp.sexp->string).
(set *maximum-print-sequence-size* 1000000000)

\\ ======================= printing compiled forms =========================
\\ Same printing approach as shen-cl's own precompile step
\\ (shen-cl/scripts/build.shen): symbols containing lowercase characters are
\\ printed in |pipes| so the case-sensitive Shen names survive the CL
\\ reader; uppercase/uncased symbols print bare.

(define lsp.sexp->string
  [] -> "()"
  true -> "|true|"
  false -> "|false|"
  Comma -> "|,|"  where (= Comma ,)
  Sym -> (lsp.symbol->string Sym)  where (symbol? Sym)
  S -> (make-string "~R" (lsp.escape-string S))  where (string? S)
  [Quote Exp] -> (@s "'" (lsp.sexp->string Exp))  where (= Quote (shen-cl.cl quote))
  [Sexp | Sexps] -> (@s "("
                        (lsp.concat-strings
                         (map (/. X (lsp.sexp->string X)) [Sexp | Sexps]))
                        ")")
  Sexp -> (make-string "~R" Sexp))

(define lsp.symbol->string
  S -> "|;|"  where (= ; S)
  S -> "|:|"  where (= : S)
  \\ A name starting with ':' (the 42.2 kernel's signature table has :=)
  \\ would read back as a KEYWORD whose name drops the colon, colliding
  \\ with = ; shen-cl's own scripts/build.shen pipe-quotes it the same way.
  S -> (@s "|" (str S) "|")  where (lsp.colon-prefixed? (explode (str S)))
  S -> (@s "|" (str S) "|")  where (lsp.cased-symbol? (explode S))
  S -> (str S))

(define lsp.colon-prefixed?
  [C | _] -> true  where (= ":" C)
  _ -> false)

(define lsp.cased-symbol?
  [] -> false
  [C | Rest] -> (or (lsp.lowercase? C) (lsp.cased-symbol? Rest)))

(define lsp.lowercase?
  C -> (let N (string->n C)
         (and (>= N 97) (<= N 122))))

(define lsp.escape-string
  S -> (lsp.escape-string-h (explode S)))

(define lsp.escape-string-h
  [] -> ""
  [C | Cs] -> (@s (n->string 92) (n->string 92) (lsp.escape-string-h Cs))
      where (= (string->n C) 92)
  [C | Cs] -> (@s (n->string 92) C (lsp.escape-string-h Cs))
      where (= (string->n C) 34)
  [C | Cs] -> (@s C (lsp.escape-string-h Cs)))

(define lsp.concat-strings
  [] -> ""
  [S] -> S
  [S | Ss] -> (@s S " " (lsp.concat-strings Ss)))

\\ ============================== helpers ==================================

(define lsp.manifest-get
  _ [] -> []
  Key [[K | Vals] | _] -> Vals  where (= Key K)
  Key [_ | Rows] -> (lsp.manifest-get Key Rows))

(define lsp.defun?
  [defun | _] -> true
  _ -> false)

\\ The compiler emits direct calls only for functions whose arity it knows;
\\ otherwise it falls back to a runtime (fn ...) lookup, which fails in the
\\ target because nothing ever stores user arities there.  So register every
\\ user defun's arity in this (host) binary before compiling anything -
\\ across all user files first, to allow cross-file references.
(define lsp.register-arities
  [] -> done
  [[defun F Args | _] | Forms] -> (do (shen.store-arity F (lsp.len Args))
                                      (lsp.register-arities Forms))
  [_ | Forms] -> (lsp.register-arities Forms))

\\ stlib's length is not always materialised in the host runtime.
(define lsp.len
  [] -> 0
  [_ | Xs] -> (+ 1 (lsp.len Xs)))

\\ "fib.kl" -> "fib"
(define lsp.strip-kl
  S -> (lsp.strip-kl-h S ""))

(define lsp.strip-kl-h
  ".kl" Acc -> Acc
  "" Acc -> Acc
  (@s C Cs) Acc -> (lsp.strip-kl-h Cs (cn Acc C)))

(define lsp.write-lsp
  File Strings -> (let Sink  (open File out)
                       Top   (pr (make-string "(in-package :shen)~%~%") Sink)
                       Write (ygg.mapc (/. S (pr (make-string "~A~%~%" S) Sink))
                                       Strings)
                       Close (close Sink)
                       File))

\\ ====================== compiling KL to Lisp =============================

\\ The shake writes the initialiser as ONE defun, a do-chain of every kept
\\ init form.  On the 42.2 kernel an eval-capable slice keeps ~300 of them,
\\ ~255 holding closures (lambda-form entries and the typechecker's
\\ signature rules), and SBCL (2.6.7 and 2.6.8 both) compiles that one
\\ function into an image save-lisp-and-die cannot write: "post-GC failure
\\ ... Verify failed".  The same forms split over a few functions save
\\ fine (shen-cl's own init.lsp is three).  So the Lisp build compiles the
\\ do-chain as consecutive chunks, each its own defun, called in order from
\\ shen.initialise: the same forms in the same order, so the same
\\ initialisation, and kernel.kl is untouched.
(set lsp.*init-chunk* 40)

(define lsp.split-initialise
  [] -> []
  [[defun shen.initialise [] Body] | Forms]
    -> (let Chunks (lsp.chunks (lsp.unchain Body) (value lsp.*init-chunk*))
            Names  (lsp.chunk-names 1 (lsp.len Chunks))
            (append (map (/. NC [defun (hd NC) [] (nest-do (hd (tl NC)))])
                         (lsp.zip Names Chunks))
                    [[defun shen.initialise [] (nest-do (map (/. N [N]) Names))]
                     | (lsp.split-initialise Forms)]))
  [F | Forms] -> [F | (lsp.split-initialise Forms)])

(define lsp.unchain
  [do A B] -> [A | (lsp.unchain B)]
  X -> [X])

(define lsp.chunks
  [] _ -> []
  Xs N -> [(lsp.take Xs N) | (lsp.chunks (lsp.drop Xs N) N)])

(define lsp.take
  [] _ -> []
  _ 0 -> []
  [X | Xs] N -> [X | (lsp.take Xs (- N 1))])

(define lsp.drop
  [] _ -> []
  Xs 0 -> Xs
  [_ | Xs] N -> (lsp.drop Xs (- N 1)))

(define lsp.chunk-names
  I N -> []  where (> I N)
  I N -> [(intern (@s "yggdrasil.initialise-" (str I))) | (lsp.chunk-names (+ I 1) N)])

(define lsp.zip
  [] [] -> []
  [X | Xs] [Y | Ys] -> [[X Y] | (lsp.zip Xs Ys)])

(define lsp.compile-kernel
  Dir -> (let Forms  (lsp.split-initialise (read-file (@s Dir "/kernel.kl")))
              FlagOn (set shen-cl.*compiling-shen-sources* true)
              Lisp   (map (/. F (shen-cl.kl->lisp F)) Forms)
              FlagOff (set shen-cl.*compiling-shen-sources* false)
              (lsp.write-lsp (@s Dir "/kernel.lsp")
                              (map (/. L (lsp.sexp->string L)) Lisp))))

(define lsp.compile-user
  Dir File -> (let Name   (lsp.strip-kl File)
                   Forms  (read-file (@s Dir "/" File))
                   Defuns (ygg.filter (/. F (lsp.defun? F)) Forms)
                   Tops   (ygg.filter (/. F (not (lsp.defun? F))) Forms)
                   CDefs  (map (/. F (shen-cl.kl->lisp F)) Defuns)
                   CTops  (map (/. F (shen-cl.kl->lisp F)) Tops)
                   Main   [(shen-cl.cl defun)
                           (intern (@s "yggdrasil.toplevel-" Name)) []
                           | CTops]
                   Strs   (map (/. L (lsp.sexp->string L))
                               (append CDefs [Main]))
                   Write  (lsp.write-lsp (@s Dir "/" Name ".lsp") Strs)
                   Name))

\\ ====================== staging the build dir ============================

(define lsp.copy-runtime
  Dir -> (do (ygg.mapc (/. F (ygg.copy-file (@s (value lsp.*shen-cl*) "src/" F)
                                            (@s Dir "/" F)))
                       (value lsp.*runtime-files*))
             (ygg.copy-file (value lsp.*driver*) (@s Dir "/driver.lsp"))))

(define lsp.write-config
  Dir Names Exe NeedsEval ->
    (lsp.write-lsp (@s Dir "/yggdrasil.config.lsp")
      (map (/. L (lsp.sexp->string L))
           [[(shen-cl.cl defparameter) (shen-cl.cl yggdrasil-user-names)
             [(shen-cl.cl quote) Names]]
            [(shen-cl.cl defparameter) (shen-cl.cl yggdrasil-exe) Exe]
            [(shen-cl.cl defparameter) (shen-cl.cl yggdrasil-needs-eval)
             (if NeedsEval (shen-cl.cl t) [])]])))

\\ Eval-capable programs (manifest needs-eval=true) need eval-kl at runtime,
\\ and shen-cl's eval-kl primitive compiles its argument with
\\ shen-cl.kl->lisp.  Stage shen-cl's precompiled compiler
\\ (compiled/compiler.lsp - generated kl->lisp output, so it is
\\ implementation-portable) and the driver loads it in boot order.
(define lsp.stage-compiler
  Dir [true] -> (do (ygg.copy-file (@s (value lsp.*shen-cl*) "compiled/compiler.lsp")
                                   (@s Dir "/compiler.lsp"))
                    true)
  _ _ -> false)

\\ ============================ entry point ================================

(define lsp.build
  Dir Exe -> (let Manifest (read-file (@s Dir "/yggdrasil.manifest"))
                  Users    (lsp.manifest-get "user" Manifest)
                  Reg      (ygg.mapc (/. F (lsp.register-arities
                                            (read-file (@s Dir "/" F))))
                                     Users)
                  Kernel   (lsp.compile-kernel Dir)
                  Names    (map (/. F (lsp.compile-user Dir F)) Users)
                  Copy     (lsp.copy-runtime Dir)
                  Eval     (lsp.stage-compiler Dir (lsp.manifest-get "needs-eval" Manifest))
                  Config   (lsp.write-config Dir Names Exe Eval)
                  (output "yggdrasil/lisp: staged ~A (users: ~A, eval: ~A) -> ~A~%"
                          Dir Names Eval Exe)))
