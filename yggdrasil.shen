\\                                           Yggdrasil
\\                  descended from Yggdrasil 1.0, (c) Mark Tarver, 3 clause BSD
\\
\\ Tree-shaker for Shen programs, targeting Mark Tarver's S42.0 (2026-08-25)
\\ kernel (shenlanguage.org, re-uploaded 2026-07-11).
\\
\\ Stage 1 (this file): shake a program against that kernel and emit
\\ minimal KL + a manifest.  Pure Shen against the certified kernel API.
\\ The reference host is shen-cl built from its S41.2-refresh master -
\\ same lineage as the vendored kernel.  A community-41.2 shen-cl is a
\\ verified-working alternative: both hosts produce byte-identical
\\ kernel.kl + manifest on all fixtures (user KL differs only in gensym
\\ numbering).  Other hosts may emit port-internal hooks from their
\\ bootstrap compiler (see README, host-portability gotcha).  Stage 2
\\ (per target, lives in each port repo): compile the shaken KL with the
\\ port's own KL->native compiler.
\\
\\ (yggdrasil.shake ["prog.shen"] "out") writes to out/:
\\    kernel.kl                shaken kernel defuns, in load order
\\    <prog>.kl                user code compiled to KL
\\    yggdrasil.manifest       sexp manifest
\\    yggdrasil.manifest.txt   line-oriented manifest (key=value)
\\
\\ Driver contract for builders: load kernel.kl, call (shen.initialise),
\\ then load the user files in order.  The S41 refresh has no
\\ shen.initialise of its own - the shake synthesises one from the
\\ kernel's toplevel init forms - so the contract is unchanged.
\\
\\ Run from the Yggdrasil directory: paths below are relative.

\\ No package wrapper: 41.2 has no stlib package to import from, and all
\\ stdlib functions are kernel-defined globals.  The public entry point is
\\ explicitly dot-qualified instead.

\\ Tarver S41.2 refresh (shenlanguage.org Download/S41.2.zip, re-uploaded
\\ 2026-07-11) in install.lsp boot order.  Unlike ShenOSKernel-41.2, this
\\ kernel has no init.kl/shen.initialise: global initialisation is toplevel
\\ forms interleaved in the files (declarations.kl sets + arity table +
\\ external-symbols put + build-lambda-table, types.kl declares).  The shake
\\ collects those forms and synthesises a (defun shen.initialise () ...)
\\ so the stage-2 builder contract is unchanged.  backend.kl (the cl.*
\\ KL->Lisp compiler) is vendored for the Lisp builder's eval path but is
\\ not part of the runtime boot, matching install.lsp.
(set *kernel* ["KLambda/sys.kl" "KLambda/writer.kl" "KLambda/core.kl"
 "KLambda/reader.kl" "KLambda/declarations.kl" "KLambda/toplevel.kl"
 "KLambda/macros.kl" "KLambda/load.kl" "KLambda/prolog.kl"
 "KLambda/sequent.kl" "KLambda/track.kl" "KLambda/t-star.kl"
 "KLambda/yacc.kl" "KLambda/types.kl"])

(set *callgraph-cache* "KLambda/callgraph-s42-20260825.shen")

\\ The 41.2 primitives: special forms plus everything the kernel calls but
\\ does not define.  Derived mechanically: symbols in call position across
\\ KLambda/*.kl minus defun'd names.  prolog-memory, vector, variable?,
\\ read-file-as-* moved into the kernel in 41.2 and are no longer here.
(set *primitives* [if and or cond defun lambda let freeze type trap-error
      cons hd tl cons? intern pos tlstr cn str string? n->string string->n
      set value simple-error error-to-string
      absvector address-> <-address absvector?
      write-byte read-byte open close get-time eval-kl
      = + - * / > < >= <= number?
      shen.char-stinput? shen.char-stoutput?
      shen.read-unit-string shen.write-string
      *stinput* *stoutput*])

\\ ===================== self-contained list helpers ======================
\\ mapc/filter/remove-duplicates/copy-file live in 41.2's stlib, which is
\\ lazily materialised and absent from port runtimes; define our own.

(define ygg.mapc
  _ [] -> done
  F [X | Xs] -> (do (F X) (ygg.mapc F Xs)))

(define ygg.filter
  _ [] -> []
  F [X | Xs] -> [X | (ygg.filter F Xs)]  where (F X)
  F [_ | Xs] -> (ygg.filter F Xs))

(define ygg.remove-dups
  [] -> []
  [X | Xs] -> (ygg.remove-dups Xs)  where (element? X Xs)
  [X | Xs] -> [X | (ygg.remove-dups Xs)])

(define ygg.copy-file
  From To -> (let Bytes (read-file-as-bytelist From)
                  Sink  (open To out)
                  Write (ygg.mapc (/. B (write-byte B Sink)) Bytes)
                  Close (close Sink)
                  To))

\\ ============================ stage 1: shake ============================

(define yggdrasil.shake
  Files Dir -> (let MaxPrint   (value *maximum-print-sequence-size*)
                    Unlimit    (set *maximum-print-sequence-size* 1000000000)
                    Kernel     (kernel-code)
                    Graph      (call-graph Kernel)
                    KLFiles    (map (fn bootstrap) Files)
                    RawKL      (map (fn read-file) KLFiles)
                    EvalFree   (eval-free? (function-calls RawKL))
                    KL         (strip-user-declares RawKL EvalFree)
                    UserFs     (function-calls KL)
                    Tops       (prepare-tops (toplevel-forms Kernel) EvalFree)
                    Graph2     (if EvalFree (strip-f-error-row Graph) Graph)
                    Seeds      (append (mapcan (fn called-fns) Tops) UserFs)
                    Foot       (footprint Seeds Graph2)
                    FootCode   (map (/. D (rewrite-f-error D EvalFree))
                                    (footcode Foot Kernel))
                    Arities    (arity-literal Tops)
                    TopsOut    (map (/. T (trim-top T Foot EvalFree Arities)) Tops)
                    InitDefun  (synthesize-initialise TopsOut)
                    OutCode    (append FootCode [InitDefun])
                    Prims      (find-primitives (append OutCode KL))
                    WriteK     (write-kl-file (@s Dir "/kernel.kl") OutCode)
                    UserOut    (write-user-files KLFiles KL Dir)
                    WriteM     (write-manifest Dir UserOut KL Prims)
                    Restore    (set *maximum-print-sequence-size* MaxPrint)
                    done))

\\ The kernel's non-defun toplevel forms, in boot order.  These ARE the
\\ initialisation in the S41 refresh; they are wrapped into a synthetic
\\ (defun shen.initialise () ...) at write time so builders keep the
\\ load-defuns / call-initialise / run-user contract.
(define toplevel-forms
  [] -> []
  [[defun | _] | Code] -> (toplevel-forms Code)
  [X | Code] -> [X | (toplevel-forms Code)])

\\ ========================== eval stripping ==============================
\\ The macro expander's registration in *macros* keeps shen.macros - and
\\ through it the typechecker, the define-compiler and eval - reachable
\\ from the init forms, putting a large floor under every program.  A
\\ compiled program only needs that machinery if it can evaluate Shen at
\\ runtime.  In the S41 refresh the registration is a toplevel form
\\ (declarations.kl), not an initialise-environment edge, so eval-free
\\ programs get it blanked in prepare-tops before seeds are computed.
\\ The 161 toplevel (declare F Type) forms in types.kl seed nothing but
\\ the typechecker's tables; eval-free programs drop them wholesale.
\\ The (shen.build-lambda-table (external shen)) form builds the
\\ name->eta-wrapper table via eval-kl at boot, which would force
\\ needs-eval=true on every program; eval-free programs get it replaced
\\ by a placeholder that trim-top expands into a literal
\\ (set shen.*lambdatable* ...) restricted to the footprint.
\\ function-calls over-approximates (every symbol counts), which errs in
\\ the safe direction: a stray symbol named eval keeps the machinery.

(set *eval-entry-points*
     [eval eval-kl load tc spy track step it
      read read-from-string lineread input input+ bootstrap])

(define eval-free?
  UserFs -> (not (intersect? UserFs (value *eval-entry-points*))))

(define intersect?
  [] _ -> false
  [X | Xs] Ys -> (or (element? X Ys) (intersect? Xs Ys)))

(define prepare-tops
  Tops false -> Tops
  Tops true  -> (ygg.filter (/. T (not (declare-form? T)))
                            (map (fn strip-eval-top) Tops)))

(define declare-form?
  [declare | _] -> true
  _ -> false)

\\ User (declare F Type) forms get the same treatment as the kernel's own,
\\ and for the same reason.  A type signature is consumed by the
\\ typechecker and by nothing else; stage 1 never runs the typechecker
\\ (bootstrap is read-file + shen->kl-h, purely syntactic - a program whose
\\ declare contradicts its define shakes without complaint today), so the
\\ signature in the emitted KL is only ever of use to a typechecker running
\\ inside the artifact.  That needs the compiler, i.e. eval.  Retained, the
\\ form is worse than dead weight: the kernel's declare calls eval-kl
\\ directly (types.kl), so a single signature drags the typechecker, the
\\ prolog engine and eval into the footprint and flips needs-eval to true -
\\ which disqualifies --web outright, since --web and --linked are mutually
\\ exclusive.  (datatype ...) already costs nothing here, but only by
\\ accident: Shen's compiler consumes it and emits no KL at all.
\\
\\ So: drop them exactly when the program is eval-free, the same gate
\\ prepare-tops uses for the kernel's 161 declares.  An eval-capable
\\ program keeps its signatures, because it may load and typecheck code at
\\ runtime; and (tc +) is itself an eval entry point, so any program that
\\ actually turns the typechecker on is never eval-free and never stripped.
(define strip-user-declares
  KL false -> KL
  KL true  -> (map (/. Forms (ygg.filter (/. F (not (declare-form? F))) Forms))
                   KL))

(define strip-eval-top
  [set *macros* _] -> [set *macros* []]
  [shen.build-lambda-table _] -> [ygg.lambdatable-placeholder]
  T -> T)

\\ Eval-free programs cannot re-enter the macro expander, so the pattern
\\ -failure row loses its edges (its body would otherwise drag the
\\ tracker/reader) ...
(define strip-f-error-row
  [] -> []
  [[shen.f-error | _] | Rows] -> [[shen.f-error] | Rows]
  [Row | Rows] -> [Row | (strip-f-error-row Rows)])

\\ ... and the defun itself is replaced by a plain error at write time.
(define rewrite-f-error
  [defun shen.f-error | _] true -> (value *static-f-error*)
  D _ -> D)

\\ In an eval-stripped program the pattern-failure handler must not offer
\\ interactive tracking (its y-or-n? prompt calls read, dragging the whole
\\ reader/typechecker/eval); it just errors.  Counterpart of the
\\ shen.f-error case in strip-f-error-row.
(set *static-f-error*
     [defun shen.f-error [V]
        [simple-error [cn [str V] ": partial function or unhandled case"]]])

\\ ====================== kernel call graph (cached) ======================
\\ The original Yggdrasil computed a full transitive closure with Warshall's
\\ algorithm - O(N^3) over every kernel symbol, which does not scale to the
\\ 41.2 kernel (683 defuns, ~280K of KL).  We only ever need reachability
\\ from a seed set, so build the direct call graph once (cached to disk)
\\ and run a worklist traversal over it per shake.  Full rationale,
\\ including why a faster external closure (Julia/bitsets) is still the
\\ wrong tool: docs/reachability.md.
\\
\\ The graph is a VALUE: a list of rows [F | Callees] threaded through the
\\ footprint computation.  The cache file is plain text - one row per line,
\\ space-separated names - parsed with string primitives.  It must NOT go
\\ through read-file: the Shen reader applies the currying transform to
\\ paren applications (and turns bracket lists into cons ASTs), silently
\\ corrupting any row whose head's declared arity differs from its length.

(define kernel-code
  -> (mapcan (fn read-kl-file) (value *kernel*)))

\\ The kernel files are already fully-expanded KL.  read-file runs them
\\ back through the macro expander, which - on the 41.2 kernel - is fatal:
\\ stlib.kl's own (defun vector.vector-macros ...) embeds literal
\\ (vector.array-> ...) sub-forms, and the live vector.vector-macros macro
\\ fires on them, trying to macro-expand a non-literal dimensional argument
\\ ((hd (tl V2049))) and aborting with "cannot macro expand the dimensional
\\ argument".  read-kl-file mirrors read-file's pipeline (raw s-exprs ->
\\ find-arities/types -> currying transform) but skips macroexpand, so
\\ already-compiled KL is read verbatim - both correct and crash-free.
(define read-kl-file
  File -> (let Bytes  (read-file-as-bytelist File)
               Sexprs (trap-error (compile (/. Z (shen.<s-exprs> Z)) Bytes)
                                  (/. E (shen.reader-error (value shen.*residue*))))
               Types  (shen.find-types Sexprs)
               (map (/. S (shen.process-applications S Types)) Sexprs)))

(define call-graph
  Code -> (trap-error (load-call-graph) (/. E (build-call-graph Code))))

\\ Loading the cache must also restore the defp marks: called-fns is used
\\ per shake on the kernel's toplevel init forms (seed collection), and
\\ kernel-defun? consults defp.  Without this, a cache-hit shake seeds
\\ nothing from the init forms and silently under-shakes (caught by the
\\ rust boot in the parity gate: "undefined function: vector").
(define load-call-graph
  -> (let Bytes (read-file-as-bytelist (value *callgraph-cache*))
          Rows  (parse-graph Bytes "" [] [])
          Mark  (ygg.mapc (/. Row (put (hd Row) defp true)) Rows)
          (if (empty? Rows) (error "empty call graph cache~%") Rows)))

\\ parse-graph Bytes Token Row Rows: accumulate chars into Token, tokens
\\ into Row, rows into Rows.  Walks the bytelist (O(n)); recursing over a
\\ string with @s patterns would copy the tail each step (O(n^2)).
(define parse-graph
  [] Token Row Rows -> (reverse (close-row Token Row Rows))
  [10 | Bs] Token Row Rows -> (parse-graph Bs "" [] (close-row Token Row Rows))
  [13 | Bs] Token Row Rows -> (parse-graph Bs Token Row Rows)
  [32 | Bs] Token Row Rows -> (parse-graph Bs "" (close-token Token Row) Rows)
  [B | Bs] Token Row Rows -> (parse-graph Bs (cn Token (n->string B)) Row Rows))

(define close-token
  "" Row -> Row
  Token Row -> [(intern Token) | Row])

(define close-row
  Token Row Rows -> (let Full (close-token Token Row)
                         (if (empty? Full) Rows [(reverse Full) | Rows])))

(define build-call-graph
  Code -> (let Fs    (defun-names Code)
               Mark  (ygg.mapc (/. F (put F defp true)) Fs)
               Graph (graph-rows Code)
               Save  (save-call-graph Graph)
               Graph))

(define defun-names
  [] -> []
  [[defun F | _] | Code] -> [F | (defun-names Code)]
  [_ | Code] -> (defun-names Code))

(define graph-rows
  [] -> []
  [[defun F _ Body] | Code] -> [[F | (called-fns Body)] | (graph-rows Code)]
  [_ | Code] -> (graph-rows Code))

\\ Several kernel data tables masquerade as code and would otherwise drag
\\ ~every public symbol into every footprint:
\\   - the arity table literal is pure name/number data;
\\   - the external-symbols registration is a name list;
\\   - a toplevel (declare F Type) uses F as a table key and Type as data
\\     - the declared function is NOT called by being declared.
\\ We drop their edges here and filter the surviving literals to the
\\ footprint at write time (see trim-top).
(define called-fns
  [shen.initialise-arity-table _] -> [shen.initialise-arity-table]
  [declare F _] -> (called-fns declare)  where (symbol? F)
  [put P shen.external-symbols _ | Rest] -> (union (called-fns put) (called-fns Rest))
      where (symbol? P)
  [set shen.*special* _] -> (called-fns set)
  [set shen.*extraspecial* _] -> (called-fns set)
  [shen.assoc-> K | R] -> (union (called-fns shen.assoc->) (called-fns R))
      where (symbol? K)
  [X | Y] -> (union (called-fns X) (called-fns Y))
  F -> [F]   where (and (symbol? F) (kernel-defun? F))
  _ -> [])

\\ defp is a build-time-only membership test: called-fns visits every
\\ symbol leaf of ~280K of KL, where (element? F Fs) over 683 names
\\ would cost ~45M comparisons.  Never consulted per-shake.
(define kernel-defun?
  F -> (trap-error (get F defp) (/. E false)))

(define save-call-graph
  Graph -> (let Sink  (open (value *callgraph-cache*) out)
                Write (ygg.mapc (/. Row (pr-graph-row Row Sink)) Graph)
                Close (close Sink)
                saved))

(define pr-graph-row
  [F | Calls] Sink -> (do (pr (str F) Sink)
                          (ygg.mapc (/. C (pr (cn " " (str C)) Sink)) Calls)
                          (pr (n->string 10) Sink)))

\\ ============================ footprint =================================
\\ Pure worklist reachability: the visited set is the accumulator itself.
\\ Seeds that are not kernel functions fall through row-calls to [].
\\ (set *use-warshall* true) routes footprint through the optional Warshall
\\ closure below instead - identical result, kept for homage to the
\\ original; see the Warshall section for the cost caveat.

(set *use-warshall* false)

(define footprint
  Seeds Graph -> (if (value *use-warshall*)
                     (warshall-footprint Seeds Graph)
                     (reach Seeds [] Graph)))

(define reach
  [] Seen _ -> Seen
  [F | Fs] Seen Graph -> (reach Fs Seen Graph)    where (element? F Seen)
  [F | Fs] Seen Graph -> (reach (append (row-calls F Graph) Fs)
                                [F | Seen] Graph))

(define row-calls
  F [[F | Calls] | _] -> Calls
  F [_ | Rows] -> (row-calls F Rows)
  _ [] -> [])

\\ ===================== Warshall closure (homage, optional) ==============
\\ Tarver's original Yggdrasil derived the footprint from the FULL transitive
\\ closure of the call graph, built with an iterative Warshall - the part he
\\ was proudest of.  His version leaned on an array/:=/for DSL that never
\\ shipped, so it could not actually run; this is the same algorithm,
\\ finished against primitives that exist (Shen vectors).  It is preserved
\\ for coherence with the original and as a differential oracle for the
\\ worklist reach (both must yield the same footprint).
\\
\\ Cost is the reason it is not the default: O(V^3) over a V*V boolean
\\ matrix.  Fine on the small graphs of the test fixtures; impractical on the
\\ full 683-node kernel (see docs/reachability.md).  Enable per run with
\\ (set *use-warshall* true).
\\
\\ One correction over the 1.0 original: each seed is unioned into its own
\\ reachable set (seed-row prepends S), so a directly-called LEAF function is
\\ retained.  The original irreflexive closure gave a leaf no row, and
\\ footprint-by-lookup then dropped it - a latent bug in 1.0.  Pivot K is the
\\ outermost loop, the invariant Warshall correctness depends on.

(define warshall-footprint
  Seeds Graph -> (let Fs    (map (fn row-head) Graph)
                      N     (length Fs)
                      Index (index-rows Fs 1)
                      M     (zero-matrix N)
                      Fill  (populate-matrix Graph M)
                      Close (warshall-iterate M N 1)
                      (collect-reachable Seeds Fs M N)))

(define row-head
  [F | _] -> F)

\\ Map each node name to its 1-based matrix index on a property list (the
\\ same trick kernel-defun? uses), so populate/collect are O(1) lookups.
(define index-rows
  [] _ -> done
  [F | Fs] I -> (do (put F ygg.warshall-ix I) (index-rows Fs (+ I 1))))

(define node-index
  F -> (trap-error (get F ygg.warshall-ix) (/. E 0)))

\\ N*N boolean matrix as a vector of N row-vectors, every cell false (Shen
\\ vector slots start unpopulated, which is not a boolean - so fill them).
(define zero-matrix
  N -> (fill-row-vectors (vector N) 1 N))

(define fill-row-vectors
  M I N -> M  where (> I N)
  M I N -> (do (vector-> M I (false-vector (vector N) 1 N))
               (fill-row-vectors M (+ I 1) N)))

(define false-vector
  V I N -> V  where (> I N)
  V I N -> (do (vector-> V I false) (false-vector V (+ I 1) N)))

(define mget
  M I J -> (<-vector (<-vector M I) J))

(define mset
  M I J Val -> (vector-> (<-vector M I) J Val))

(define populate-matrix
  [] _ -> done
  [[F | Calls] | Rows] M -> (do (populate-row (node-index F) Calls M)
                                (populate-matrix Rows M)))

(define populate-row
  _ [] _ -> done
  I [C | Cs] M -> (do (set-edge I (node-index C) M) (populate-row I Cs M)))

(define set-edge
  _ 0 _ -> done            \\ callee is not a graph node (e.g. a primitive)
  I J M -> (mset M I J true))

\\ Iterative Warshall: pivot K outermost, then I, then J -
\\ M[I][J] |= M[I][K] and M[K][J].  The I-loop skips rows where M[I][K] is
\\ false (nothing to propagate), matching the guard in the 1.0 original.
(define warshall-iterate
  M N K -> M  where (> K N)
  M N K -> (do (warshall-i M N K 1) (warshall-iterate M N (+ K 1))))

(define warshall-i
  M N K I -> done  where (> I N)
  M N K I -> (do (if (mget M I K) (warshall-j M N K I 1) done)
                 (warshall-i M N K (+ I 1))))

(define warshall-j
  M N K I J -> done  where (> J N)
  M N K I J -> (do (if (mget M K J) (mset M I J true) done)
                   (warshall-j M N K I (+ J 1))))

(define collect-reachable
  [] _ _ _ -> []
  [S | Ss] Fs M N -> (union (seed-row S Fs M N) (collect-reachable Ss Fs M N)))

\\ A seed always contributes itself (the worklist adds every seed to Seen,
\\ kernel node or not); a kernel seed also contributes its closure row.
(define seed-row
  S Fs M N -> (let I (node-index S)
                   (if (= I 0) [S] [S | (row-true I Fs M 1 N)])))

(define row-true
  _ _ _ J N -> []  where (> J N)
  I Fs M J N -> [(nth J Fs) | (row-true I Fs M (+ J 1) N)]  where (mget M I J)
  I Fs M J N -> (row-true I Fs M (+ J 1) N))

(define function-calls
  [X | Y] -> (union (function-calls X) (function-calls Y))
  V -> []   where (variable? V)
  F -> [F]  where (symbol? F)
  _ -> [])

(define footcode
  Footprint Kernel -> (ygg.filter (/. Def (mentioned? Def Footprint)) Kernel))

\\ Write-time rewrites of the kept toplevel init forms.  When
\\ eval-stripping, the arity-table and external-symbols literals are
\\ restricted to the footprint (plus primitives): an eval-stripped program
\\ can never define or look up functions outside its footprint, and the
\\ full literal both bloats kernel.kl and re-introduces stray names
\\ (eval-kl among them) that find-primitives would report.  The
\\ lambdatable placeholder from strip-eval-top becomes a literal
\\ (set shen.*lambdatable* ...) of eta-wrappers for footprint functions -
\\ what build-lambda-table would have built with eval-kl at boot.
(define trim-top
  [shen.initialise-arity-table Lit] Foot true _ ->
      [shen.initialise-arity-table (trim-arity-pairs Lit (keep-set Foot))]
  [put P shen.external-symbols Lit V] Foot true _ ->
      [put P shen.external-symbols (trim-sym-list Lit (keep-set Foot)) V]
  [ygg.lambdatable-placeholder] Foot true Arities ->
      [set shen.*lambdatable* (consify (lambdatable-entries Foot Arities))]
  T _ _ _ -> T)

\\ The synthetic initialiser: the kept toplevel forms, in boot order, as a
\\ right-nested do-chain.  Builders call it exactly as they called 41.2's
\\ shen.initialise.
(define synthesize-initialise
  Tops -> [defun shen.initialise [] (nest-do Tops)])

(define nest-do
  [] -> []
  [T] -> T
  [T | Ts] -> [do T (nest-do Ts)])

\\ The arity-table literal from the toplevel forms (flat alternating
\\ (cons Name (cons Arity ...)) list); the eta-entry generator reads
\\ arities out of it.
(define arity-literal
  [] -> []
  [[shen.initialise-arity-table Lit] | _] -> Lit
  [_ | Tops] -> (arity-literal Tops))

(define arity-of
  F [cons F [cons A _]] -> A
  F [cons _ [cons _ Rest]] -> (arity-of F Rest)
  _ _ -> -1)

\\ Literal eta-wrapper entries, replacing boot-time eval-kl: for each
\\ footprint function of arity N >= 1, (cons F (lambda X1 .. (F X1..XN))),
\\ plus build-lambda-table's five hardwired printer entries when they are
\\ in the footprint.  Deterministic variable names keep kernel.kl
\\ byte-identical across hosts.
(define lambdatable-entries
  Foot Arities -> (append
                   (mapcan (/. F (eta-hardwired F Foot))
                           [shen.tuple shen.pvar shen.print-prolog-vector
                            shen.print-freshterm shen.printF])
                   (mapcan (/. F (eta-if-fn F Arities)) Foot)))

(define eta-hardwired
  F Foot -> (if (element? F Foot) [(eta-entry F 1)] []))

(define eta-if-fn
  F Arities -> (let A (arity-of F Arities)
                    (if (> A 0) [(eta-entry F A)] [])))

(define eta-entry
  F N -> (let Vars (eta-vars 1 N)
             [cons F (eta-nest Vars [F | Vars])]))

(define eta-vars
  I N -> []  where (> I N)
  I N -> [(intern (cn "X" (str I))) | (eta-vars (+ I 1) N)])

(define eta-nest
  [] App -> App
  [V | Vs] App -> [lambda V (eta-nest Vs App)])

(define consify
  [] -> []
  [X | Xs] -> [cons X (consify Xs)])

\\ Names worth keeping in the stripped data tables: footprint plus
\\ primitives, minus the eval entry points (unreachable by construction).
(define keep-set
  Foot -> (ygg.filter (/. F (not (element? F (value *eval-entry-points*))))
                      (append Foot (value *primitives*))))

(define trim-arity-pairs
  [cons Name [cons Arity Rest]] Keep ->
      (if (element? Name Keep)
          [cons Name [cons Arity (trim-arity-pairs Rest Keep)]]
          (trim-arity-pairs Rest Keep))
  X _ -> X)

(define trim-sym-list
  [cons Name Rest] Keep -> (if (element? Name Keep)
                               [cons Name (trim-sym-list Rest Keep)]
                               (trim-sym-list Rest Keep))
  X _ -> X)

(define mentioned?
  [defun F | _] Fs -> (element? F Fs)
  _ _ -> false)

\\ ============================ primitives ================================

(define find-primitives
  X -> [X]     where (primitive? X)
  [X | Y] -> (union (find-primitives X) (find-primitives Y))
  _ -> [])

(define primitive?
  {symbol --> boolean}
  X -> (element? X (value *primitives*)))

\\ ---------------------------- capabilities ------------------------------
\\ Group the effectful gateway primitives by capability.  A shaken program
\\ "cannot reach" a capability when the emitted KL contains none of its
\\ gateways - a static, certifiable property of the artifact (the code
\\ literally has no occurrence of the gateway).  Derived from the same
\\ primitive set the manifest already reports (find-primitives over the
\\ footprint + user KL), so it stays in lock-step with the eval-strip:
\\ eval-kl drops out of Prims exactly when the program is eval-free, and
\\ cannot-reach then lists eval.  Reported as reaches=/cannot-reach= lines;
\\ builders ignore keys they do not recognise.
(set *capabilities*
  [[eval  eval-kl]
   [read  read-byte]
   [write write-byte]
   [file  open close]
   [clock get-time]])

(define cap-label
  [L | _] -> L)

(define cap-reached?
  [_ | Sinks] Prims -> (intersect? Sinks Prims))

(define reaches-caps
  Prims -> (map (fn cap-label)
                (ygg.filter (/. C (cap-reached? C Prims)) (value *capabilities*))))

(define cannot-reach-caps
  Prims -> (map (fn cap-label)
                (ygg.filter (/. C (not (cap-reached? C Prims))) (value *capabilities*))))

\\ Used by backends that map primitives to copyable implementation files
\\ (the Tarver model, retained for the Lisp backend).
(define primfiles
  Primitives Language -> (ygg.remove-dups
                          (mapcan (/. Primitive (get Primitive Language)) Primitives)))

(define copy-primitive-files
  {(list string) --> string --> (list string)}
  Files Dir -> (map (/. X (copy-primitive-file X Dir)) Files))

(define copy-primitive-file
  {string --> string --> string}
  File Dir -> (let Truncate (truncate-filename File "")
                   Copy (ygg.copy-file File (@s Dir "/" Truncate))
                   Truncate))

(define truncate-filename
  {string --> string --> string}
  "" Out -> Out
  (@s "/" S) _ -> (truncate-filename S "")
  (@s S Ss) Out -> (truncate-filename Ss (cn Out S)))

\\ ========================== writing KL files ============================
\\ Shen's printer renders lists in Shen syntax ([...]), but .kl files must
\\ be in KL syntax ((...)), so we print cons trees ourselves.

(define write-kl-file
  File Code -> (let Sink  (open File out)
                    Write (ygg.mapc (/. X (do (pr-kl X Sink)
                                          (pr (make-string "~%~%") Sink))) Code)
                    Close (close Sink)
                    File))

(define pr-kl
  [] Sink -> (pr "()" Sink)
  [X | Xs] Sink -> (do (pr "(" Sink) (pr-kl X Sink) (pr-kl-body Xs Sink))
  \\ The kernel writer renders anything = to (fail) as "..." (see
  \\ shen.arg->str), which is unreadable KL; print the marker verbatim.
  X Sink -> (pr "shen.fail!" Sink)  where (= X (fail))
  X Sink -> (pr (make-string "~S" X) Sink))

(define pr-kl-body
  [] Sink -> (pr ")" Sink)
  [X | Xs] Sink -> (do (pr " " Sink) (pr-kl X Sink) (pr-kl-body Xs Sink)))

(define pr-kl-line
  X Sink -> (do (pr-kl X Sink) (pr (make-string "~%") Sink)))

(define write-user-files
  [] [] _ -> []
  [File | Files] [Code | Codes] Dir ->
     (let Name  (truncate-filename File "")
          Write (write-kl-file (@s Dir "/" Name) Code)
          [Name | (write-user-files Files Codes Dir)]))

\\ ============================ manifest ==================================
\\ Contract additions requested by every stage-2 builder so far:
\\   fn=<name> <arity>    one per user defun, so builders need not rescan
\\                        the KL (arity bugs were the #1 stage-2 trap)
\\   global=              *stinput* etc., split out of primitive=
\\   primitive-optional=  guarded-dead unless the port's char-st*
\\                        predicates return true; a port may omit them
\\ Builders must ignore keys they do not recognise.

(set *optional-primitives* [shen.write-string shen.read-unit-string])
(set *global-primitives*   [*stinput* *stoutput*])

(define write-manifest
  Dir UserFiles UserKL Prims ->
     (let NeedsEval (element? eval-kl Prims)
          Fns       (user-arities UserKL)
          Globals   (ygg.filter (/. P (element? P (value *global-primitives*))) Prims)
          Optional  (ygg.filter (/. P (element? P (value *optional-primitives*))) Prims)
          Required  (ygg.filter (/. P (not (or (element? P Globals)
                                               (element? P Optional)))) Prims)
          Reaches   (reaches-caps Prims)
          Cannot    (cannot-reach-caps Prims)
          Sexp (write-manifest-sexp Dir UserFiles Fns Required Optional Globals NeedsEval Reaches Cannot)
          Txt  (write-manifest-txt Dir UserFiles Fns Required Optional Globals NeedsEval Reaches Cannot)
          done))

(define user-arities
  [] -> []
  [[[defun F Args | _] | Forms] | Files] -> [[F (ygg.len Args)]
                                             | (user-arities [Forms | Files])]
  [[_ | Forms] | Files] -> (user-arities [Forms | Files])
  [[] | Files] -> (user-arities Files))

(define ygg.len
  [] -> 0
  [_ | Xs] -> (+ 1 (ygg.len Xs)))

(define write-manifest-sexp
  Dir UserFiles Fns Required Optional Globals NeedsEval Reaches Cannot ->
    (let Sink (open (@s Dir "/yggdrasil.manifest") out)
         W1 (pr-kl-line ["yggdrasil-manifest" 3] Sink)
         W2 (pr-kl-line ["kernel-version" "42-s42.20260825"] Sink)
         W3 (pr-kl-line ["kernel" "kernel.kl"] Sink)
         W4 (pr-kl-line ["init" shen.initialise] Sink)
         W5 (pr-kl-line ["user" | UserFiles] Sink)
         W6 (ygg.mapc (/. FA (pr-kl-line ["fn" | FA] Sink)) Fns)
         W7 (pr-kl-line ["primitives" | Required] Sink)
         W8 (pr-kl-line ["primitives-optional" | Optional] Sink)
         W9 (pr-kl-line ["globals" | Globals] Sink)
         WA (pr-kl-line ["needs-eval" NeedsEval] Sink)
         WB (pr-kl-line ["reaches" | Reaches] Sink)
         WC (pr-kl-line ["cannot-reach" | Cannot] Sink)
         (close Sink)))

(define write-manifest-txt
  Dir UserFiles Fns Required Optional Globals NeedsEval Reaches Cannot ->
    (let Sink (open (@s Dir "/yggdrasil.manifest.txt") out)
         W1 (pr (make-string "manifest-version=3~%") Sink)
         W2 (pr (make-string "kernel-version=42-s42.20260825~%") Sink)
         W3 (pr (make-string "kernel=kernel.kl~%") Sink)
         W4 (pr (make-string "init=shen.initialise~%") Sink)
         W5 (ygg.mapc (/. F (pr (make-string "user=~A~%" F) Sink)) UserFiles)
         W6 (ygg.mapc (/. FA (pr (make-string "fn=~A ~A~%" (hd FA) (hd (tl FA))) Sink)) Fns)
         W7 (ygg.mapc (/. P (pr (make-string "primitive=~A~%" P) Sink)) Required)
         W8 (ygg.mapc (/. P (pr (make-string "primitive-optional=~A~%" P) Sink)) Optional)
         W9 (ygg.mapc (/. P (pr (make-string "global=~A~%" P) Sink)) Globals)
         WA (pr (make-string "needs-eval=~A~%" NeedsEval) Sink)
         WB (ygg.mapc (/. C (pr (make-string "reaches=~A~%" C) Sink)) Reaches)
         WC (ygg.mapc (/. C (pr (make-string "cannot-reach=~A~%" C) Sink)) Cannot)
         (close Sink)))
