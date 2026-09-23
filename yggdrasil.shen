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

\\ The call-graph cache is a DERIVED artifact and deliberately does NOT live
\\ in KLambda/.  It used to, and main.go's go:embed then said `KLambda` --
\\ a bare directory pattern, which sweeps up whatever is in the working tree
\\ including gitignored generated files.  So the cache rode into the binary,
\\ into embeddedHash() (which names the extracted root), and into every root:
\\ the shaker's behaviour depended on untracked files present at `go build`
\\ time, and two builds of one commit were not equivalent.  Since a cache is
\\ keyed by filename alone, a stale one loaded fine and SILENTLY UNDER-SHOOK
\\ (see the defp note on load-call-graph).
\\
\\ Both halves are now closed.  main.go names `KLambda/*.kl` explicitly, so
\\ nothing generated under KLambda/ can be embedded even if it reappears; and
\\ at the repo root this file matches no embed pattern at all.  The CLI's
\\ extracted root is named for a hash of the embedded tree, so a cache
\\ written inside one is, by construction, the cache for that kernel.
\\ portable_test.go's TestEmbeddedTreeHasNoGeneratedCache keeps it so.
\\
\\ A content check in Shen is not an option: folding over the 324 KB of
\\ KLambda as a bytelist measured ~5 s per pass on the shen-go host, against
\\ the ~0.24 s the cache saves there.  Freshness has to be structural.
(set *callgraph-cache* "callgraph-cache.shen")

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

\\ ========================= the shared prelude ===========================
\\ Four entry points - the shake itself and the footprints, facts and
\\ trace-check reports - all open with the SAME analysis: read the kernel
\\ and the user's files, decide the eval mode from the user's own symbols,
\\ run the shake rules, and read the footprint back out of the database.
\\ They differ only in what they do with it afterwards, so that prelude is
\\ computed once, here, and handed over as one record.
\\
\\ The record is an association list of [Key Value] rows - the same shape
\\ this file already uses for facts rows - read back through ygg.pl and the
\\ named accessors below.  Evaluation order inside the `let` is the order
\\ the four preludes had, and that order is load-bearing twice over:
\\ `bootstrap` compiles the user's .shen files, and ygg.shake-rules-run
\\ leaves the Datalog database standing for ygg.computed-names, ygg.dl-col1
\\ and trace-check's own ygg.dl-add to read.
\\
\\ *maximum-print-sequence-size* is part of the prelude too: each caller
\\ lifted it first and put it back last, so ygg.pipeline lifts it and keeps
\\ the old value in the record for ygg.pl-restore.
\\
\\ Two entry points are deliberately NOT callers.  yggdrasil.shake-full
\\ builds no call graph, runs no rule set and keeps the user KL unstripped,
\\ and ygg.why-h derives its two footprints from the worklist rather than
\\ from the rules; giving either this prelude would make it run a rule set
\\ it does not read.

(define ygg.pipeline
  Files -> (let MaxPrint (value *maximum-print-sequence-size*)
                Unlimit  (set *maximum-print-sequence-size* 1000000000)
                Kernel   (kernel-code)
                Graph    (call-graph Kernel)
                KLFiles  (map (fn bootstrap) Files)
                RawKL    (map (fn read-file) KLFiles)
                RawFs    (function-calls RawKL)
                EvalFree (eval-free? RawFs)
                KL       (strip-user-declares RawKL EvalFree)
                AllTops  (toplevel-forms Kernel)
                Tops     (prepare-tops AllTops EvalFree)
                Seeds    (append (mapcan (fn called-fns) Tops) (function-calls KL))
                Run      (ygg.shake-rules-run Kernel Graph AllTops KL RawFs)
                Foot     (ygg.rule-footprint Seeds Graph)
                [[maxprint MaxPrint] [kernel Kernel] [graph Graph]
                 [klfiles KLFiles] [rawfs RawFs] [evalfree EvalFree]
                 [kl KL] [alltops AllTops] [tops Tops] [seeds Seeds]
                 [foot Foot]]))

\\ One field of a pipeline record.  A key that is not in it matches no
\\ clause, so a mistyped accessor is a loud error rather than a silent [].
(define ygg.pl
  K [[Key V] | _] -> V  where (= K Key)
  K [_ | P] -> (ygg.pl K P))

(define ygg.pl-kernel   P -> (ygg.pl kernel P))
(define ygg.pl-graph    P -> (ygg.pl graph P))
(define ygg.pl-klfiles  P -> (ygg.pl klfiles P))
(define ygg.pl-rawfs    P -> (ygg.pl rawfs P))
(define ygg.pl-evalfree P -> (ygg.pl evalfree P))
(define ygg.pl-kl       P -> (ygg.pl kl P))
(define ygg.pl-alltops  P -> (ygg.pl alltops P))
(define ygg.pl-tops     P -> (ygg.pl tops P))
(define ygg.pl-seeds    P -> (ygg.pl seeds P))
(define ygg.pl-foot     P -> (ygg.pl foot P))

\\ Put *maximum-print-sequence-size* back to what ygg.pipeline found.
(define ygg.pl-restore
  P -> (set *maximum-print-sequence-size* (ygg.pl maxprint P)))

\\ ============================ stage 1: shake ============================

(define yggdrasil.shake
  Files Dir -> (let P          (ygg.pipeline Files)
                    Kernel     (ygg.pl-kernel P)
                    KLFiles    (ygg.pl-klfiles P)
                    RawFs      (ygg.pl-rawfs P)
                    EvalFree   (ygg.pl-evalfree P)
                    KL         (ygg.pl-kl P)
                    Tops       (ygg.pl-tops P)
                    Foot       (ygg.pl-foot P)
                    CNames     (ygg.computed-names)
                    Warn       (ygg.cn-warn CNames)
                    FootCode   (map (/. D (rewrite-f-error D EvalFree))
                                    (footcode Foot Kernel))
                    Arities    (arity-literal Tops)
                    TopsOut    (map (/. T (trim-top T Foot EvalFree Arities)) Tops)
                    \\ reach is read out of the database BEFORE the
                    \\ init-order check: that check closes a rule set of
                    \\ its own, and ygg.dl-run starts from an empty
                    \\ database.
                    Reach      (ygg.dl-col1 reach)
                    InitOrder  (ygg.init-order-check TopsOut KL)
                    Dead       (ygg.dead-init Kernel Reach TopsOut KL RawFs)
                    KeptTops   (ygg.prune-init TopsOut Dead)
                    NPruned    (- (ygg.len TopsOut) (ygg.len KeptTops))
                    InitOrder2 (ygg.init-order-recheck TopsOut KeptTops KL
                                                       InitOrder)
                    InitDefun  (synthesize-initialise KeptTops)
                    \\ Weaving is the LAST thing before writing, and in
                    \\ particular after dead-init pruning: a trace has to
                    \\ describe the artifact as shipped, so it must see the
                    \\ initialiser that survived, not the one that did not.
                    OutCode    (ygg.trace-kernel (append FootCode [InitDefun]))
                    UserKL     (ygg.trace-user KL)
                    Prims      (find-primitives (append OutCode UserKL))
                    WriteK     (write-kl-file (@s Dir "/kernel.kl") OutCode)
                    UserOut    (write-user-files KLFiles UserKL Dir)
                    WriteM     (write-manifest Dir UserOut UserKL Prims CNames
                                               NPruned InitOrder2)
                    Restore    (ygg.pl-restore P)
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

\\ ====================== build-time typecheck gate =======================
\\ Stage 1 never runs the typechecker: bootstrap is read-file + shen->kl-h,
\\ purely syntactic, and strip-user-declares drops signatures from eval-free
\\ programs - so a program whose types are wrong shakes without complaint.
\\ (yggdrasil.check Files) closes that gap: run BEFORE a shake (in its own
\\ host process), it loads each file's forms under (tc +) semantics -
\\ mirroring shen.check-eval-and-print from load.kl - and reports the first
\\ type failure with per-form blame.  Erasure is preserved: a passing check
\\ changes nothing about the shake, which still strips declares and consumes
\\ inline signatures syntactically.
\\
\\ Both typed styles are accepted.  The kernel's own typetable only honours
\\ inline {A --> B} signatures and hard-errors "missing { in F" on unsigned
\\ defines, so a toplevel (declare F Type) - the strip-friendly style - is
\\ first folded into its define as an inline signature, then the standalone
\\ declare form is dropped (its information now travels with the define).
\\
\\ Like the kernel's work-through, each form is typechecked and THEN
\\ evaluated, so later forms see earlier defines and datatypes.  Toplevel
\\ side effects of the checked program therefore execute during the check;
\\ run it in a throwaway process (the Go driver does).
\\
\\ Output contract (the Go side trusts these sentinels, never exit codes):
\\   yggdrasil-check: OK files=N inferences=I version=V
\\   yggdrasil-check: FAIL file=F form=N name=G   + reason + truncated form

(define yggdrasil.check
  Files -> (let Tc     (set shen.*tc* true)
                Result (trap-error (do (ygg.mapc (/. F (ygg.check-file F)) Files)
                                       ok)
                                   (/. E (ygg.check-trap E)))
                Off    (set shen.*tc* false)
                (if (= Result ok)
                    (do (pr (make-string "yggdrasil-check: OK files=~A inferences=~A version=~A~%"
                                         (ygg.len Files)
                                         (trap-error (inferences) (/. E "?"))
                                         (trap-error (value *version*) (/. E "?")))
                            (stoutput))
                        done)
                    check-failed)))

\\ check-fail already printed its own FAIL sentinel before raising the abort
\\ marker; any other error (unreadable file, host trouble) gets a generic
\\ FAIL line here so the Go driver always has a sentinel to parse.
(define ygg.check-trap
  E -> failed  where (= "yggdrasil-check-abort" (error-to-string E))
  E -> (do (pr (make-string "yggdrasil-check: FAIL file=? form=? name=?~%  ~A~%"
                            (error-to-string E))
               (stoutput))
           failed))

(define ygg.check-file
  File -> (let Forms  (read-file File)
               Sigs   (ygg.collect-declares Forms)
               Merged (ygg.merge-declares Forms Sigs)
               Pairs  (ygg.signature-pass File Merged 1)
               Assume (shen.assumetypes Pairs)
               (ygg.check-forms File Merged 1)))

\\ [[F Type] ...] for every toplevel (declare F Type).
(define ygg.collect-declares
  [] -> []
  [[declare F Type] | Forms] -> [[F Type] | (ygg.collect-declares Forms)]
      where (symbol? F)
  [_ | Forms] -> (ygg.collect-declares Forms))

\\ Fold declared signatures into their unsigned defines as inline { ... }
\\ groups (top-level splice: nested type sub-expressions stay as sublists,
\\ which shen.type-F accepts), and drop the standalone declare forms.
(define ygg.merge-declares
  [] _ -> []
  [[declare F _] | Forms] Sigs -> (ygg.merge-declares Forms Sigs)
      where (symbol? F)
  [[define F | Rest] | Forms] Sigs ->
      [(ygg.merge-define F Rest (ygg.declared-type F Sigs))
       | (ygg.merge-declares Forms Sigs)]
  [Form | Forms] Sigs -> [Form | (ygg.merge-declares Forms Sigs)])

(define ygg.declared-type
  _ [] -> none
  F [[F Type] | _] -> [found Type]
  F [_ | Sigs] -> (ygg.declared-type F Sigs))

(define ygg.merge-define
  F Rest none -> [define F | Rest]
  F Rest _    -> [define F | Rest]
      where (and (cons? Rest) (= (intern "{") (hd Rest)))
  F Rest [found Type] ->
      [define F (intern "{") | (append Type [(intern "}") | Rest])])

\\ Signature pass with blame: every define must carry an inline signature by
\\ now (native or merged).  Returns the flat [F1 T1 F2 T2 ...] list that
\\ shen.assumetypes consumes (rectified by the kernel's own typetable).
(define ygg.signature-pass
  _ [] _ -> []
  File [[define F | Rest] | Forms] N ->
      (append (trap-error (shen.typetable [define F | Rest])
                          (/. E (ygg.check-fail File N F (error-to-string E)
                                                [define F | Rest])))
              (ygg.signature-pass File Forms (+ N 1)))
      where (and (cons? Rest) (= (intern "{") (hd Rest)))
  File [[define F | Rest] | Forms] N ->
      (ygg.check-fail File N F
        "no type signature: under the typecheck gate every define needs an inline {A --> B} signature or a toplevel (declare F Type)"
        [define F | Rest])
  File [_ | Forms] N -> (ygg.signature-pass File Forms (+ N 1)))

\\ work-through with blame: typecheck each form against a fresh variable,
\\ then evaluate it so later forms see it.  shen.typecheck returns false
\\ (no reason) on ordinary failures, so blame is file + form + name.
(define ygg.check-forms
  _ [] _ -> done
  File [Form | Forms] N -> (do (ygg.check-form File Form N)
                               (ygg.check-forms File Forms (+ N 1))))

(define ygg.check-form
  File Form N ->
    (let Name (ygg.form-name Form)
         T    (trap-error (shen.typecheck Form (intern "A"))
                          (/. E (ygg.check-fail File N Name
                                                (error-to-string E) Form)))
         (if (= T false)
             (ygg.check-fail File N Name "type error" Form)
             (let Run (trap-error (eval-kl (shen.shen->kl Form))
                                  (/. E (ygg.check-fail File N Name
                                          (make-string "evaluation failed: ~A"
                                                       (error-to-string E))
                                          Form)))
                  (pr (make-string "  ~A : ~R~%" Name (shen.pretty-type T))
                      (stoutput))))))

(define ygg.form-name
  [define F | _] -> F
  [datatype F | _] -> F
  [Op | _] -> Op  where (symbol? Op)
  _ -> toplevel)

(define ygg.check-fail
  File N Name Reason Form ->
    (do (pr (make-string "yggdrasil-check: FAIL file=~A form=~A name=~A~%"
                         File N Name)
            (stoutput))
        (pr (make-string "  ~A~%" Reason) (stoutput))
        (pr (make-string "  form: ~A~%"
                         (ygg.take-chars (make-string "~R" Form) 200))
            (stoutput))
        (simple-error "yggdrasil-check-abort")))

(define ygg.take-chars
  _ 0 -> "..."
  S N -> (if (= S "")
             ""
             (@s (pos S 0) (ygg.take-chars (tlstr S) (- N 1)))))

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

\\ ====================== ygg.dl: a small Datalog engine ==================
\\ Stage 3 of docs/analysis-rules.md.  The shake's footprint is no longer a
\\ hand-written traversal: it is the least fixpoint of the rule set in
\\ (value *shake-rules*) - the same rules analysis/analysis.dl gives to
\\ Souffle - over facts the shake already extracts (ygg.cls-defuns,
\\ ygg.mention-rows, function-calls).  The worklist `reach` above and the
\\ Warshall closure below stay as differential oracles; yggdrasil.footprints
\\ runs all three and compares.
\\
\\ A tuple is a list [Pred Arg ...].  A rule is [Head | Body]; a body
\\ literal is a tuple pattern (Shen variables stand for logic variables),
\\ [not Lit] for stratified negation, or [ne A B] for an inequality guard.
\\ A program is a LIST OF STRATA, each a list of rules, evaluated in order:
\\ a [not P] literal may only name a P whose stratum is already closed, and
\\ ygg.dl-check-neg refuses a program that breaks that.
\\
\\ Two things make it fast enough to sit inside a shake.  (1) The database
\\ is indexed, not scanned: every tuple is filed under a property of the
\\ interned key "ygg.dl/Pred/Arg1" (the same put/get trick called-fns uses
\\ for defp), so a goal whose first argument is bound - (edge F G) with F
\\ known, which is every step of the reach recursion - costs one hash
\\ lookup instead of a walk of the whole database.  (2) Evaluation is
\\ semi-naive: after the first round, a rule is re-fired only with one of
\\ its body literals drawn from the tuples that were NEW in the previous
\\ round, so no join is recomputed against tuples it already saw.
\\
\\ The database lives in the property store rather than in a threaded
\\ value: Shen has no hash type in the certified API, and an assoc list
\\ over 686 kernel names would put the scan back.  ygg.*dl-keys* records
\\ every key touched so ygg.dl-reset can empty them again - a run starts
\\ from a clean database even inside one host process.

(set ygg.*dl-keys* [])

(define ygg.dl-key
  Pred Arg -> (intern (cn "ygg.dl/" (cn (ygg.fact-str Pred)
                                        (cn "/" (ygg.fact-str Arg))))))

(define ygg.dl-get
  Key -> (trap-error (get Key ygg.dl) (/. E [])))

(define ygg.dl-reset
  -> (do (ygg.mapc (/. K (put K ygg.dl [])) (value ygg.*dl-keys*))
         (set ygg.*dl-keys* [])
         done))

\\ A nullary or first-argument-free goal still needs a bucket; ygg.dl-all is
\\ the per-predicate bucket every tuple also goes into, for goals whose
\\ first argument is unbound.
(define ygg.dl-first
  [] -> ygg.dl-nullary
  [A | _] -> A)

(define ygg.dl-push
  K T -> (let Old (ygg.dl-get K)
              Note (if (empty? Old)
                       (set ygg.*dl-keys* [K | (value ygg.*dl-keys*)])
                       Old)
              (put K ygg.dl [T | Old])))

\\ true when the tuple was not already there - which is what makes it part
\\ of the next round's delta.
(define ygg.dl-add
  [P | Args] -> (let K (ygg.dl-key P (ygg.dl-first Args))
                     (if (element? [P | Args] (ygg.dl-get K))
                         false
                         (do (ygg.dl-push K [P | Args])
                             (ygg.dl-push (ygg.dl-key P ygg.dl-all) [P | Args])
                             true))))

(define ygg.dl-query
  P -> (ygg.dl-get (ygg.dl-key P ygg.dl-all)))

\\ The second column of a two-column relation, which is what every relation
\\ the shake reads back out of the engine happens to be.
(define ygg.dl-col1
  P -> (map (/. T (hd (tl T))) (ygg.dl-query P)))

\\ ------------------------------ unification -----------------------------
\\ Substitutions are assoc lists of [Var | Term]; walk chases bindings.

\\ A logic variable is a MARKED TERM, [ygg.dl-lv Name], not a Shen variable.
\\ The engine must never decide varness by looking at a symbol's spelling:
\\ Shen's `variable?` is true of every uppercase-initial symbol, so a fact
\\ argument that happens to be spelled that way - a KL let-variable reaching
\\ `reads`, say - would become a wildcard that unifies with anything and
\\ disables the first-argument index.  ygg.dl-varify is the only producer of
\\ the marker and ygg.dl-lvar? the only recogniser, so data is data whatever
\\ it is called.  Matching on the head alone keeps the test one cons? and one
\\ eq on the hot path.
(define ygg.dl-lvar?
  [ygg.dl-lv | _] -> true
  _ -> false)

(define ygg.dl-lookup
  V [] -> V
  V [[V | T] | _] -> T
  V [_ | S] -> (ygg.dl-lookup V S))

(define ygg.dl-walk
  X S -> (let Y (ygg.dl-lookup X S) (if (= X Y) X (ygg.dl-walk Y S)))
      where (ygg.dl-lvar? X)
  [X | Xs] S -> [(ygg.dl-walk X S) | (ygg.dl-walk Xs S)]
  X _ -> X)

(define ygg.dl-unify
  X Y S -> (ygg.dl-unify-h (ygg.dl-walk X S) (ygg.dl-walk Y S) S))

(define ygg.dl-unify-h
  X X S -> S
  V T S -> [[V | T] | S]  where (ygg.dl-lvar? V)
  T V S -> [[V | T] | S]  where (ygg.dl-lvar? V)
  [X | Xs] [Y | Ys] S -> (let S1 (ygg.dl-unify X Y S)
                              (if (= S1 fail) fail (ygg.dl-unify Xs Ys S1)))
  _ _ _ -> fail)

\\ ------------------------------- solving --------------------------------
\\ (ygg.dl-solve Body S N Delta): every substitution extending S under which
\\ Body holds.  N is the 0-based index of the body literal to draw from
\\ Delta instead of from the database (-1 for none) - the semi-naive
\\ restriction.  Candidates for an ordinary literal come from the index when
\\ its first argument is already bound, and from the predicate's ygg.dl-all
\\ bucket otherwise.

(define ygg.dl-solve
  [] S _ _ -> [S]
  [[not G] | Gs] S N D -> (if (empty? (ygg.dl-solve [G] S -1 []))
                              (ygg.dl-solve Gs S (- N 1) D)
                              [])
  [[ne A B] | Gs] S N D -> (if (= (ygg.dl-walk A S) (ygg.dl-walk B S))
                               []
                               (ygg.dl-solve Gs S (- N 1) D))
  [G | Gs] S 0 D -> (ygg.dl-extend Gs S -1 D (ygg.dl-unifiers G (ygg.dl-delta G D) S))
  [G | Gs] S N D -> (ygg.dl-extend Gs S (- N 1) D
                                   (ygg.dl-unifiers G (ygg.dl-cands G S) S)))

(define ygg.dl-extend
  Gs S N D Subs -> (mapcan (/. S1 (ygg.dl-solve Gs S1 N D)) Subs))

(define ygg.dl-unifiers
  G Cands S -> (ygg.filter (/. X (not (= X fail)))
                           (map (/. F (ygg.dl-unify G F S)) Cands)))

(define ygg.dl-cands
  [P | Args] S -> (let A (ygg.dl-walk (ygg.dl-first Args) S)
                       (if (ygg.dl-lvar? A)
                           (ygg.dl-query P)
                           (ygg.dl-get (ygg.dl-key P A)))))

(define ygg.dl-delta
  [P | _] D -> (ygg.filter (/. T (= (hd T) P)) D))

\\ ------------------------------ evaluation ------------------------------
\\ Round 0 fires every rule against the whole database; later rounds fire
\\ each rule once per body position that mentions a predicate of THIS
\\ stratum, with that position restricted to the previous round's delta.
\\ A rule with no such position is complete after round 0.

(define ygg.dl-derive
  [Head | Body] N D -> (map (/. S (ygg.dl-walk Head S)) (ygg.dl-solve Body [] N D)))

(define ygg.dl-new
  Ts -> (ygg.filter (fn ygg.dl-add) Ts))

(define ygg.dl-positions
  [] _ _ -> []
  [[not _] | Ls] N Preds -> (ygg.dl-positions Ls (+ N 1) Preds)
  [[ne _ _] | Ls] N Preds -> (ygg.dl-positions Ls (+ N 1) Preds)
  [[P | _] | Ls] N Preds -> [N | (ygg.dl-positions Ls (+ N 1) Preds)]
      where (element? P Preds)
  [_ | Ls] N Preds -> (ygg.dl-positions Ls (+ N 1) Preds))

(define ygg.dl-rule-round
  [Head | Body] D Preds -> (mapcan (/. N (ygg.dl-derive [Head | Body] N D))
                                   (ygg.dl-positions Body 0 Preds)))

(define ygg.dl-round
  Rules D Preds -> (ygg.dl-new (mapcan (/. R (ygg.dl-rule-round R D Preds)) Rules)))

(define ygg.dl-loop
  _ _ [] -> done
  Rules Preds D -> (ygg.dl-loop Rules Preds (ygg.dl-round Rules D Preds)))

(define ygg.dl-head-pred
  [[P | _] | _] -> P)

\\ Stratification is the one thing a Datalog with negation can get silently
\\ wrong, so it is checked rather than assumed.  A [not P] is legal only
\\ where P is already CLOSED - an EDB relation, or the head of a stratum
\\ that has finished - so the check is against the closed set, not against
\\ this stratum's own heads.  Checking only the latter would pass a [not P]
\\ whose P is derived in a LATER stratum, which reads an empty relation and
\\ is the silent wrong answer the check exists to catch.
(define ygg.dl-check-neg
  Rules Closed -> (ygg.mapc (/. R (ygg.dl-check-rule R Closed)) Rules))

(define ygg.dl-check-rule
  [_ | Body] Closed -> (ygg.mapc (/. L (ygg.dl-check-lit L Closed)) Body))

(define ygg.dl-check-lit
  [not [P | _]] Closed -> (if (element? P Closed)
                              done
                              (simple-error (cn "ygg.dl: unstratified negation on "
                                                (str P))))
  _ _ -> done)

(define ygg.dl-stratum
  Rules Closed -> (let Check (ygg.dl-check-neg Rules Closed)
                       Preds (map (fn ygg.dl-head-pred) Rules)
                       D0    (ygg.dl-new (mapcan (/. R (ygg.dl-derive R -1 [])) Rules))
                       (ygg.dl-loop Rules Preds D0)))

\\ (ygg.dl-strata Closed Strata) closes each stratum in turn, growing the
\\ closed set by that stratum's head predicates as it goes.
(define ygg.dl-strata
  _ [] -> done
  Closed [Rules | Ss] -> (do (ygg.dl-stratum Rules Closed)
                             (ygg.dl-strata (append (map (fn ygg.dl-head-pred) Rules)
                                                    Closed)
                                            Ss)))

\\ The predicate names of an EDB: what is closed before the first stratum
\\ runs.  A relation with no facts at all is not nameable here, and is not
\\ closed - a [not P] on one is a typo, not a negation.
(define ygg.dl-fact-preds
  Facts -> (ygg.remove-dups (map (fn hd) Facts)))

\\ (ygg.dl-run Facts Strata) loads the EDB and closes each stratum in turn.
\\ The database is left standing for the caller's queries; the next run
\\ clears it.
(define ygg.dl-run
  Facts Strata -> (do (ygg.dl-reset)
                      (ygg.mapc (fn ygg.dl-add) Facts)
                      (ygg.dl-strata (ygg.dl-fact-preds Facts) Strata)
                      done))

\\ =========================== the shake's rules ==========================
\\ analysis/analysis.dl, as Shen data, stratum by stratum.  It is meant to
\\ be read against that file line for line: same relation names, same
\\ clause order, same deviations D1-D7 (see the header there).  The only
\\ syntactic difference is that logic variables are written as the
\\ lowercase names below and turned into marked terms [ygg.dl-lv F] by
\\ ygg.dl-varify: the authoring syntax is lowercase because Shen's `define`
\\ rejects free variables in a body and a literal [reach G] at toplevel
\\ would be one, and the marker is what the engine recognises, so no fact
\\ argument can be mistaken for a variable by its spelling.
\\
\\   evalcapable(S) :- rawsym(S), entry(S).
\\   anyeval(1)     :- evalcapable(_).
\\   evalfree(1)    :- !anyeval(1).
\\   edge(F, G)     :- callpos(F, G), kernel(G), F != "shen.f-error".
\\   edge(F, G)     :- argpos(F, G, _), kernel(G), F != "shen.f-error".
\\   edge("shen.f-error", G) :- callpos("shen.f-error", G), kernel(G), anyeval(1).
\\   edge("shen.f-error", G) :- argpos("shen.f-error", G, _), kernel(G), anyeval(1).
\\   floorseed(G)   :- formmentionsef(_, G), kernel(G), evalfree(1).
\\   floorseed(G)   :- formmentions(_, G), kernel(G), anyeval(1).
\\   seed(G)        :- floorseed(G).
\\   seed(G)        :- usersym(G), kernel(G), evalfree(1).
\\   seed(G)        :- rawsym(G), kernel(G), anyeval(1).
\\   reach(G)       :- seed(G).
\\   reach(G)       :- reach(F), edge(F, G).
\\   floor(G)       :- floorseed(G).
\\   floor(G)       :- floor(F), edge(F, G).
\\   computedName(F) :- userintern(F).
\\   computedName(F) :- userglobal(F).
\\
\\ Strata: the mode first (evalcapable, anyeval; then evalfree, which is
\\ the one negated literal), then the edges, then the seeds, then the two
\\ fixpoints, then the stage-3 computed-name hypothesis.  Nothing in a
\\ stratum negates a predicate its own stratum derives.
\\
\\ Body-literal order is chosen for the index, not for the reading: the
\\ literal that binds the first argument of the next one comes first, so
\\ reach's recursive clause walks edges by hash lookup.  The declarative
\\ meaning is order-independent, so this costs the comparison nothing.
\\
\\ usedprim/reaches/needsEval and datasym are in analysis.dl but have no
\\ rule here: the shake computes the primitive set with find-primitives
\\ over the code it is about to WRITE (which includes the synthesised
\\ initialiser, D7), and datasym derives nothing by D2.  Both are dumped as
\\ facts for the oracle, which does evaluate them.

(set ygg.*dl-vars* [[f "F"] [g "G"] [c "C"] [m "M"] [n "N"] [s "S"] [v "V"]])

(define ygg.dl-var
  X [] -> X
  X [[X Name] | _] -> [ygg.dl-lv (intern Name)]
  X [_ | Vs] -> (ygg.dl-var X Vs))

(define ygg.dl-varify
  [X | Y] -> [(ygg.dl-varify X) | (ygg.dl-varify Y)]
  X -> (ygg.dl-var X (value ygg.*dl-vars*))  where (symbol? X)
  X -> X)

\\ ----------------------------- engine selftest --------------------------
\\ (yggdrasil.dl-selftest) is a test-only entry point, driven by
\\ dlengine_test.go the way (yggdrasil.footprints ...) drives the footprint
\\ comparison: it prints one sentinel line and the sentinel decides.
\\
\\   yggdrasil-dl-selftest: OK cases=N
\\   yggdrasil-dl-selftest: FAIL <case>
\\
\\ Every case asserts a property the engine used to get WRONG, so none of
\\ them can pass under the old behaviour:
\\   - a datum spelled with a leading uppercase is data, not a wildcard: it
\\     keys the index and refuses to unify with anything else;
\\   - a [not P] is legal only where P is already closed - an EDB relation
\\     or the head of an EARLIER stratum - so a later-stratum P is an error
\\     rather than a silent read of an empty relation;
\\   - and the legal negation the shake's own rules depend on still runs,
\\     including over a closed relation that happens to be empty.
(define ygg.dl-t-errors?
  F -> (trap-error (do (thaw F) false) (/. E true)))

\\ (a) a ground argument keys the index however it is spelled: the bucket
\\ for X is the one tuple, not the whole three-tuple relation.
(define ygg.dl-t-index
  -> (let Load (ygg.dl-run [[kernel (intern "X")] [kernel (intern "Y")] [kernel abc]] [])
          (and (= 3 (ygg.len (ygg.dl-query kernel)))
               (= [[kernel (intern "X")]] (ygg.dl-cands [kernel (intern "X")] [])))))

\\ (a) and it is not a wildcard: [kernel X] and [kernel abc] do not unify.
(define ygg.dl-t-no-unify
  -> (let Load (ygg.dl-run [[kernel abc]] [])
          (and (= fail (ygg.dl-unify [kernel (intern "X")] [kernel abc] []))
               (= fail (ygg.dl-unify [kernel (intern "X")] [kernel (intern "Y")] [])))))

\\ (a) a rule over those facts derives exactly the two tuples.
(define ygg.dl-t-rule-over-data
  -> (let Load (ygg.dl-run [[kernel (intern "X")] [kernel (intern "Y")]]
                           [(ygg.dl-varify [[[p g] [kernel g]]])])
          (and (= 2 (ygg.len (ygg.dl-query p)))
               (and (element? [p (intern "X")] (ygg.dl-query p))
                    (element? [p (intern "Y")] (ygg.dl-query p))))))

\\ (a) the explosion the finding names: a recursive relation holding an
\\ uppercase-spelled datum must not match every edge in the database.
\\ Old behaviour derived treach for d and h as well.
(define ygg.dl-t-no-explosion
  -> (let Load (ygg.dl-run [[tseed (intern "X")] [tedge c d] [tedge e h]]
                           (ygg.dl-varify [[[[treach g] [tseed g]]
                                            [[treach g] [treach f] [tedge f g]]]]))
          (= [[treach (intern "X")]] (ygg.dl-query treach))))

\\ (b) a [not P] whose P is derived in a LATER stratum is an error, not a
\\ silent read of an empty relation.  Old behaviour derived tbad(1).
(define ygg.dl-t-later-neg
  -> (ygg.dl-t-errors?
      (freeze (ygg.dl-run [[tbase 1]]
                          (ygg.dl-varify [[[[tearly 1] [tbase 1]]]
                                          [[[tbad 1]   [not [tlate 1]]]]
                                          [[[tlate 1]  [tbase 1]]]])))))

\\ (b) same-stratum negation is still refused, as it always was.
(define ygg.dl-t-same-neg
  -> (ygg.dl-t-errors?
      (freeze (ygg.dl-run [[tbase 1]]
                          (ygg.dl-varify [[[[tq 1] [not [tq 1]]]]])))))

\\ (b) and a [not P] on a relation nothing declares is refused too: an
\\ undeclared relation is a typo, not a negation over the empty set.
(define ygg.dl-t-undeclared-neg
  -> (ygg.dl-t-errors?
      (freeze (ygg.dl-run [[tbase 1]]
                          (ygg.dl-varify [[[[tr 1] [not [tnosuch 1]]]]])))))

\\ (c) the legal case the shake's own rules are built on: evalfree(1) :-
\\ !anyeval(1), with anyeval closed by an earlier stratum.  Both modes,
\\ because the eval-free one negates a closed relation that is EMPTY - the
\\ case a check keyed on "has tuples" rather than "is closed" would break.
(define ygg.dl-t-legal-neg
  -> (let Rules (ygg.dl-varify [[[[evalcapable s] [rawsym s] [entry s]]
                                 [[anyeval 1] [evalcapable s]]]
                                [[[evalfree 1] [not [anyeval 1]]]]])
          Free  (do (ygg.dl-run [[rawsym a]] Rules) (ygg.dl-query evalfree))
          Cap   (do (ygg.dl-run [[rawsym a] [entry a]] Rules) (ygg.dl-query evalfree))
          (and (= [[evalfree 1]] Free) (empty? Cap))))

\\ the marker, and only the marker, is what the engine calls a variable.
(define ygg.dl-t-marker
  -> (and (ygg.dl-lvar? (ygg.dl-varify g))
          (and (not (ygg.dl-lvar? (intern "G")))
               (not (ygg.dl-lvar? [kernel (intern "G")])))))

(define yggdrasil.dl-selftest
  -> (let Cases [[ground-keys-the-index   (ygg.dl-t-index)]
                 [ground-does-not-unify   (ygg.dl-t-no-unify)]
                 [rule-over-data-symbols  (ygg.dl-t-rule-over-data)]
                 [no-wildcard-explosion   (ygg.dl-t-no-explosion)]
                 [later-stratum-negation  (ygg.dl-t-later-neg)]
                 [same-stratum-negation   (ygg.dl-t-same-neg)]
                 [undeclared-negation     (ygg.dl-t-undeclared-neg)]
                 [legal-earlier-negation  (ygg.dl-t-legal-neg)]
                 [marker-is-the-variable  (ygg.dl-t-marker)]]
          Bad   (ygg.filter (/. C (not (hd (tl C)))) Cases)
          (ygg.dl-selftest-report Bad (ygg.len Cases))))

(define ygg.dl-selftest-report
  [] N -> (do (pr (make-string "yggdrasil-dl-selftest: OK cases=~A~%" N) (stoutput))
              done)
  [[Name _] | _] _ -> (do (pr (make-string "yggdrasil-dl-selftest: FAIL ~A~%" Name)
                              (stoutput))
                          done))

(set *shake-rules*
  (ygg.dl-varify
   [\\ mode
    [[[evalcapable s] [rawsym s] [entry s]]
     [[anyeval 1]     [evalcapable s]]]
    [[[evalfree 1]    [not [anyeval 1]]]]
    \\ edges (D1: position-insensitive; D2: datasym derives nothing;
    \\        D3: shen.f-error's whole row is mode-gated)
    [[[edge f g]              [callpos f g] [kernel g] [ne f shen.f-error]]
     [[edge f g]              [argpos f g c] [kernel g] [ne f shen.f-error]]
     [[edge shen.f-error g]   [callpos shen.f-error g] [kernel g] [anyeval 1]]
     [[edge shen.f-error g]   [argpos shen.f-error g c] [kernel g] [anyeval 1]]]
    \\ seeds (D4, D5: both readings are facts, the mode picks one)
    [[[floorseed g] [formmentionsef n g] [kernel g] [evalfree 1]]
     [[floorseed g] [formmentions n g]   [kernel g] [anyeval 1]]
     [[seed g]      [floorseed g]]
     [[seed g]      [usersym g] [kernel g] [evalfree 1]]
     [[seed g]      [rawsym g]  [kernel g] [anyeval 1]]]
    \\ reach and floor (D6: the recursion is restricted to kernel defuns)
    [[[reach g] [seed g]]
     [[reach g] [reach f] [edge f g]]
     [[floor g] [floorseed g]]
     [[floor g] [floor f] [edge f g]]]
    \\ the stage-3 computed-name hypothesis: warns, decides nothing
    [[[computedName f] [userintern f]]
     [[computedName f] [userglobal f]]]]))

\\ ===================== the rules as the shake's footprint ===============
\\ The facts are the ones the fact dump already extracts (ygg.cls-defuns,
\\ ygg.mention-rows, function-calls), so the shake and the Souffle oracle
\\ read the same relations off the same code.  Mode-dependent relations are
\\ handed over in BOTH readings (D4, D5) and the rules pick, exactly as the
\\ dump does - the engine is given no idea which mode it is in beyond the
\\ entry/rawsym facts.

(define ygg.shake-edb
  Kernel Graph AllTops KL RawFs
   -> (append (map (/. R [kernel (row-head R)]) Graph)
      (append (ygg.cls-defuns Kernel)
      (append (map (/. R [formmentions | R]) (ygg.mention-rows AllTops 1 false))
      (append (map (/. R [formmentionsef | R]) (ygg.mention-rows AllTops 1 true))
      (append (map (/. S [usersym S]) (ygg.remove-dups (function-calls KL)))
      (append (map (/. S [rawsym S]) (ygg.remove-dups RawFs))
      (append (map (/. S [entry S]) (value *eval-entry-points*))
              (ygg.cn-facts KL)))))))))

\\ Run the rules.  Leaves the database standing so the callers below can
\\ read reach, floor and computedName out of it.
(define ygg.shake-rules-run
  Kernel Graph AllTops KL RawFs
   -> (ygg.dl-run (ygg.shake-edb Kernel Graph AllTops KL RawFs)
                  (value *shake-rules*)))

\\ The rules decide WHICH kernel defuns are in the footprint.  The ORDER of
\\ the list they are rendered in is not something Datalog derives - Datalog
\\ derives a set - so it is DEFINED here, as a value rather than as a second
\\ traversal: kernel load order, i.e. the order graph-rows walked the kernel
\\ in, which is what (map (fn row-head) Graph) reads back off.
\\
\\   Foot = [ F in kernel load order | reach F ]
\\          ++ [ S in Seeds, deduplicated | not (reach S) ]
\\
\\ ygg.remove-dups keeps the LAST occurrence of a repeated name, so the
\\ remainder is in last-occurrence Seeds order; deterministic either way,
\\ and nothing downstream reads that order.
\\
\\ The remainder is exactly the NON-kernel seeds - primitives, user function
\\ names, data symbols - because every kernel-defun seed is in `reach` by the
\\ seed -> reach rule; they carry no row in Graph and so cannot appear in the
\\ first half, and keep-set / trim-arity-pairs / eta-if-fn read them, so they
\\ are not droppable.  Membership is therefore unchanged from the depth-first
\\ walk this replaced: that walk emitted F only when F was in Reach u Seeds,
\\ emitted every seed (each starts in its worklist and every seed is in the
\\ set), and was asserted to emit every reach tuple - so its answer was
\\ Reach u Seeds as a set, which is what the two halves above produce.
\\
\\ The order is observable in exactly one place: lambdatable-entries walks
\\ this list to build the (set shen.*lambdatable* ...) literal that lands in
\\ the synthesised shen.initialise.  footcode filters the kernel by
\\ MEMBERSHIP, so every defun in kernel.kl is written in kernel load order
\\ whatever this list says, and fn looks the lambdatable up with assoc, so
\\ the order carries no meaning beyond the bytes.  Recorded as deviation D8
\\ in analysis/analysis.dl.
(define ygg.rule-footprint
  Seeds Graph -> (let Reached (ygg.filter (fn ygg.dl-reach?)
                                          (map (fn row-head) Graph))
                      Extra   (ygg.filter (/. S (not (ygg.dl-reach? S)))
                                          (ygg.remove-dups Seeds))
                      (append Reached Extra)))

\\ One lookup in the engine's index, not an element? scan over a 600-element
\\ list.  reach is one-column, so its ygg.dl-key bucket holds [reach F] and
\\ nothing else.
(define ygg.dl-reach?
  F -> (not (empty? (ygg.dl-get (ygg.dl-key reach F)))))

\\ ------------------------- computed names (stage 3) ---------------------
\\ The soundness argument for the whole shake is that a name the artifact
\\ can call is a name that occurs syntactically in the code.  Two things
\\ break that: `intern`, which turns a string into a callable symbol, and a
\\ (value X) / (set X _) whose X is not a literal symbol, which reaches a
\\ global whose name is only known at runtime.  Neither is an eval entry
\\ point - a program that interns a name it never applies is perfectly
\\ safe - so this stage only REPORTS: one
\\   yggdrasil-shake: WARN computed-name in F
\\ line per user defun (or `top` for a file's toplevel forms) that contains
\\ one, and a computed-names= key in both manifests.  Deciding what to do
\\ about it is a later stage's problem; recording that the hypothesis is
\\ testable, and which fixtures test it, is this one's.

(define ygg.cn-facts
  KL -> (mapcan (fn ygg.cn-file) KL))

(define ygg.cn-file
  Forms -> (append (mapcan (fn ygg.cn-form) Forms)
                   (ygg.cn-of top (toplevel-forms Forms))))

(define ygg.cn-form
  [defun F _ Body] -> (ygg.cn-of F Body)
  _ -> [])

(define ygg.cn-of
  F Body -> (append (if (ygg.cn-intern? Body) [[userintern F]] [])
                    (if (ygg.cn-global? Body) [[userglobal F]] [])))

\\ In KL every occurrence of `intern` is an application or an argument
\\ handed to one, so "occurs at all" and "occurs in an applied or argument
\\ position" are the same test here.
(define ygg.cn-intern?
  [X | Y] -> (or (ygg.cn-intern? X) (ygg.cn-intern? Y))
  intern -> true
  _ -> false)

(define ygg.cn-global?
  [value V] -> true  where (not (ygg.cn-literal-sym? V))
  [set V _] -> true  where (not (ygg.cn-literal-sym? V))
  [X | Y] -> (or (ygg.cn-global? X) (ygg.cn-global? Y))
  _ -> false)

\\ A KL variable is a symbol too, so symbol? alone would call (value V2049)
\\ a literal name - the very case this is looking for.
(define ygg.cn-literal-sym?
  V -> (and (symbol? V) (not (variable? V))))

\\ Derivation order is the database's, i.e. reversed; source order reads
\\ better in a manifest and is just as deterministic.
(define ygg.computed-names
  -> (reverse (ygg.remove-dups (ygg.dl-col1 computedName))))

(define ygg.cn-warn
  Names -> (ygg.mapc (/. F (pr (make-string "yggdrasil-shake: WARN computed-name in ~A~%" F)
                               (stoutput)))
                     Names))

(define ygg.cn-report
  [] -> "none"
  Names -> (ygg.cn-commas Names))

(define ygg.cn-commas
  [F] -> (str F)
  [F | Fs] -> (cn (str F) (cn "," (ygg.cn-commas Fs))))

\\ ----------------------- the three-way differential ---------------------
\\ Test-only entry point (initorder_test.go's sibling, footprint_test.go).
\\ The same footprint from the rules, from the worklist `reach`, and from
\\ the Warshall closure; the three must be the same set or stage 3 has
\\ broken something.  The Warshall leg runs over the graph restricted to
\\ the footprint - the closure of a set closed under edges is unchanged by
\\ dropping the rest - because O(V^3) over all 686 kernel nodes is minutes
\\ (see the Warshall section); above ygg.*warshall-limit* nodes it is
\\ skipped and says so.
\\
\\   yggdrasil-footprints: mode=M rules=N worklist=N warshall=N agree=true
\\   yggdrasil-footprint-order: F1 F2 ...
\\   yggdrasil-kernel-order: K1 K2 ...
\\
\\ The Warshall count is deduplicated before it is printed: collect-reachable
\\ unions one row per seed, so a seed that is also somebody's callee appears
\\ twice - a multiset, not a disagreement.  agree= is a set comparison.
\\
\\ The two order lines carry the footprint LIST and kernel load order, so a
\\ test can check the order ygg.rule-footprint DEFINES and not only the
\\ membership the rules derive: the footprint restricted to kernel defuns
\\ must be a subsequence of kernel load order.

(set ygg.*warshall-limit* 150)

(define yggdrasil.footprints
  Files -> (let P        (ygg.pipeline Files)
                Graph    (ygg.pl-graph P)
                EvalFree (ygg.pl-evalfree P)
                Seeds    (ygg.pl-seeds P)
                Rules    (ygg.pl-foot P)
                Graph2   (if EvalFree (strip-f-error-row Graph) Graph)
                Work     (reach Seeds [] Graph2)
                Wars     (ygg.warshall-leg Seeds Graph2 Rules)
                Report   (pr (make-string
                              "yggdrasil-footprints: mode=~A rules=~A worklist=~A warshall=~A agree=~A~%"
                              (if EvalFree "eval-free" "eval-capable")
                              (ygg.len Rules) (ygg.len Work)
                              (if (= Wars skipped)
                                  "skipped"
                                  (ygg.len (ygg.remove-dups Wars)))
                              (and (ygg.same-set? Rules Work)
                                   (or (= Wars skipped) (ygg.same-set? Rules Wars))))
                             (stoutput))
                Order    (pr (make-string "yggdrasil-footprint-order: ~A~%"
                                          (ygg.fp-join Rules))
                             (stoutput))
                Load     (pr (make-string "yggdrasil-kernel-order: ~A~%"
                                          (ygg.fp-join (map (fn row-head) Graph)))
                             (stoutput))
                Restore  (ygg.pl-restore P)
                done))

\\ The two order lines are for footprint_test.go: it checks, outside this
\\ file, that the footprint restricted to kernel defuns is a SUBSEQUENCE of
\\ kernel load order.  Emitting both lists rather than a Shen-computed
\\ verdict keeps the judgement on the Go side, where a regression in
\\ ygg.rule-footprint cannot also silence its own check.
(define ygg.fp-join
  [] -> ""
  [F] -> (str F)
  [F | Fs] -> (cn (str F) (cn " " (ygg.fp-join Fs))))

(define ygg.warshall-leg
  Seeds Graph Foot -> skipped  where (> (ygg.len Foot) (value ygg.*warshall-limit*))
  Seeds Graph Foot -> (warshall-footprint
                       Seeds
                       (ygg.filter (/. R (element? (row-head R) Foot)) Graph)))

(define ygg.same-set?
  Xs Ys -> (and (empty? (ygg.filter (/. X (not (element? X Ys))) Xs))
                (empty? (ygg.filter (/. Y (not (element? Y Xs))) Ys))))

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
  Dir UserFiles UserKL Prims CNames NPruned InitOrder ->
     (let NeedsEval (element? eval-kl Prims)
          Computed  (ygg.cn-report CNames)
          Fns       (user-arities UserKL)
          Globals   (ygg.filter (/. P (element? P (value *global-primitives*))) Prims)
          Optional  (ygg.filter (/. P (element? P (value *optional-primitives*))) Prims)
          Required  (ygg.filter (/. P (not (or (element? P Globals)
                                               (element? P Optional)))) Prims)
          Reaches   (reaches-caps Prims)
          Cannot    (cannot-reach-caps Prims)
          Sexp (write-manifest-sexp Dir UserFiles Fns Required Optional Globals NeedsEval Computed NPruned Reaches Cannot InitOrder)
          Txt  (write-manifest-txt Dir UserFiles Fns Required Optional Globals NeedsEval Computed NPruned Reaches Cannot InitOrder)
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
  Dir UserFiles Fns Required Optional Globals NeedsEval Computed NPruned Reaches Cannot InitOrder ->
    (let Sink (open (@s Dir "/yggdrasil.manifest") out)
         W1 (pr-kl-line ["yggdrasil-manifest" 4] Sink)
         W2 (pr-kl-line ["kernel-version" "42-s42.20260825"] Sink)
         W3 (pr-kl-line ["kernel" "kernel.kl"] Sink)
         W4 (pr-kl-line ["init" shen.initialise] Sink)
         W5 (pr-kl-line ["user" | UserFiles] Sink)
         W6 (ygg.mapc (/. FA (pr-kl-line ["fn" | FA] Sink)) Fns)
         W7 (pr-kl-line ["primitives" | Required] Sink)
         W8 (pr-kl-line ["primitives-optional" | Optional] Sink)
         W9 (pr-kl-line ["globals" | Globals] Sink)
         WA (pr-kl-line ["needs-eval" NeedsEval] Sink)
         WA2 (pr-kl-line ["init-order" InitOrder] Sink)
         WA3 (pr-kl-line ["computed-names" Computed] Sink)
         WA4 (pr-kl-line ["pruned-init" NPruned] Sink)
         WB (pr-kl-line ["reaches" | Reaches] Sink)
         WC (pr-kl-line ["cannot-reach" | Cannot] Sink)
         WD (ygg.shaken-line-sexp Sink)
         WE (ygg.trace-manifest-sexp Sink)
         (close Sink)))

(define write-manifest-txt
  Dir UserFiles Fns Required Optional Globals NeedsEval Computed NPruned Reaches Cannot InitOrder ->
    (let Sink (open (@s Dir "/yggdrasil.manifest.txt") out)
         W1 (pr (make-string "manifest-version=4~%") Sink)
         W2 (pr (make-string "kernel-version=42-s42.20260825~%") Sink)
         W3 (pr (make-string "kernel=kernel.kl~%") Sink)
         W4 (pr (make-string "init=shen.initialise~%") Sink)
         W5 (ygg.mapc (/. F (pr (make-string "user=~A~%" F) Sink)) UserFiles)
         W6 (ygg.mapc (/. FA (pr (make-string "fn=~A ~A~%" (hd FA) (hd (tl FA))) Sink)) Fns)
         W7 (ygg.mapc (/. P (pr (make-string "primitive=~A~%" P) Sink)) Required)
         W8 (ygg.mapc (/. P (pr (make-string "primitive-optional=~A~%" P) Sink)) Optional)
         W9 (ygg.mapc (/. P (pr (make-string "global=~A~%" P) Sink)) Globals)
         WA (pr (make-string "needs-eval=~A~%" NeedsEval) Sink)
         WA2 (pr (make-string "init-order=~A~%" InitOrder) Sink)
         WA3 (pr (make-string "computed-names=~A~%" Computed) Sink)
         WA4 (pr (make-string "pruned-init=~A~%" NPruned) Sink)
         WB (ygg.mapc (/. C (pr (make-string "reaches=~A~%" C) Sink)) Reaches)
         WC (ygg.mapc (/. C (pr (make-string "cannot-reach=~A~%" C) Sink)) Cannot)
         WD (ygg.shaken-line-txt Sink)
         WE (ygg.trace-manifest-txt Sink)
         (close Sink)))

\\ ======================= initialisation order check =====================
\\ Stage 2 of docs/analysis-rules.md.  The kept toplevel forms are the
\\ program's initialisation, in order: the kernel's own init forms first
\\ (as trim-top leaves them), then, after the synthesised initialiser, the
\\ user files' toplevel forms in manifest order.  A form may evaluate
\\ (value V) only if some form up to and INCLUDING itself wrote V - either
\\ directly, with its own (set V _), or indirectly, by applying a function
\\ whose body sets V - or V is a port global (*stinput*, *stoutput* - the
\\ port supplies those before any Shen code runs).  Reads inside a defun do
\\ not count: a defun body runs when it is called, not while the artifact
\\ boots.
\\
\\ The indirect half is the point.  A program that says
\\     (define setup -> (set *cfg* 41))
\\     (setup)
\\     (print (value *cfg*))
\\ initialises *cfg* perfectly well, and a check that only ever looks for a
\\ literal toplevel (set *cfg* _) refuses it with a hint - "reorder the
\\ program" - that cannot be acted on, because there is nothing to reorder.
\\ So the write side is closed over the call graph: `writesVia` is the
\\ transitive closure of "F's body sets V, or F applies something that
\\ does", and a toplevel form covers V when it applies such an F.
\\
\\ Two more programs run correctly and were refused for the same reason, so
\\ the write side widens twice more:
\\     (do (set *x* 1) (print (value *x*)))
\\     (thaw (freeze (set *f* 1)))  ...  (print (value *f*))
\\ The first has no EARLIER form to point at - the write and the read are
\\ one toplevel form - so a form's own writes discharge its own reads.  The
\\ second writes inside a freeze, and ygg.io-writes stops at a freeze on
\\ purpose (stage 4 must not read an unthawed freeze as an initialisation),
\\ so the write side gets its own deeper walk, `formwrite`.
\\
\\ All three widenings are over-approximations - a call COULD write V but
\\ ignores branches, guards and argument values; nothing here knows in which
\\ order a `do` runs or whether a freeze is ever thawed - so a read that
\\ only they discharge is discharged more weakly than one a literal,
\\ strictly earlier (set V _) discharges.  That difference is itself derived
\\ - weakRead, below, against directWrittenBefore, which stays strictly
\\ earlier and freeze-blind - and the manifests report it: init-order=checked
\\ when every read had a direct writer, init-order=checked-weak when at
\\ least one needed a widening.  What stays refused is a read with no write
\\ anywhere up to and including its own form, which is exactly the program
\\ a reordering can fix.
\\
\\ This is a Datalog rule set (analysis/analysis.dl, "initialisation order"),
\\ evaluated here by ygg.dl and by analysis/refeval.py and Souffle over the
\\ facts `yggdrasil facts` dumps, and checked across engines the way reach,
\\ computedName and deadInit are: TestAnalysisOracleMatchesShake diffs
\\ readBeforeWrite and weakRead refeval-against-the-shake on every fixture,
\\ and souffle-against-refeval whenever Souffle is on PATH.  Souffle is NOT
\\ installed on every dev box - a local `go test` then proves two of the
\\ three - and the nightly analysis-oracle workflow, which is where Souffle
\\ actually parses this file, diffs `reach` rather than these two relations.
\\ The order arrives as an EDB
\\ relation rather than a `<` side condition precisely so that ygg.dl -
\\ which has no arithmetic - can evaluate the rules unchanged.
\\
\\ That EDB is `succ`, the LINEAR one: succ(M,M+1) for each adjacent pair,
\\ and writtenBefore walks it transitively.  The first version of this
\\ check used the obvious quadratic spelling - a before(M,N) tuple for
\\ every M < N, 20,301 of them for tests/metaeval.shen's 202 forms - which
\\ derives the identical relation and cost about 0.12s per run just to load,
\\ ygg.dl-add being a dedupe against a bucket.  succ costs 201 tuples.
\\
\\ The bigger cost was the closure itself, and `wantedGlobal` is what fixes
\\ it: see the rule set below.  The third was running the whole check twice
\\ per shake for the same answer, which ygg.init-order-recheck now avoids
\\ when stage 4 pruned nothing (every shake without --prune-init).
\\ Together, measured on this machine as user CPU over three runs:
\\ metaeval 1.02-1.07s at d1d1e23, ~1.9s with the first version of this
\\ check, 0.91-0.94s here; partial-eval 0.94-1.04 -> ~2.0 -> 0.96-1.09;
\\ fib 0.83-0.86 -> 0.84-0.91; interpreter 1.01-1.07 -> 1.16-1.25.  The
\\ stage-2 check is back to costing about what it cost before it became a
\\ rule set, and it now decides strictly more.
\\
\\ The check runs after trim-top and before anything is written, so a
\\ violation aborts the shake with no kernel.kl - the Go driver's "missing
\\ or empty kernel.kl means failure" contract.  On a violation it prints
\\   yggdrasil-shake: FAIL init-order form=N reads=V
\\ (N is the 1-based index in the final form sequence) and errors out; the
\\ violation reported is the EARLIEST one, so the message points at the
\\ first form that cannot run rather than an arbitrary member of the set.

\\ Returns the manifest's init-order= value: `checked` when the direct half
\\ of the rule discharged every read on its own, `checked-weak` when at least
\\ one read needed one of the three widenings.  A return value rather than a
\\ process global: the caller that writes the manifest is the caller that
\\ ran the check, and the answer belongs to a particular form sequence -
\\ a global would silently record whichever of the shake's two checks ran
\\ last.
(define ygg.init-order-check
  Tops UserKL
   -> (let Forms  (append Tops (ygg.user-tops UserKL))
           Rows   (ygg.io-rows Forms)
           Defuns (ygg.io-defuns UserKL)
           Run    (ygg.dl-run (ygg.io-edb Forms Rows Defuns)
                              (value ygg.*init-order-rules*))
           Bad    (ygg.io-least (ygg.dl-query readBeforeWrite) [])
           Report (ygg.io-report Bad)
           (ygg.io-status)))

\\ The shake checks the form sequence twice: once as trim-top leaves it, and
\\ once after stage 4 has pruned dead initialisation, because pruning shifts
\\ the indices `succ` is built from.  Pruning only DROPS forms, though, and
\\ with (value ygg.*prune-init*) false it drops none - so when the two
\\ sequences are the same sequence the second run closes the same four strata
\\ over the same EDB to reach the same answer.  Reusing the first answer
\\ there halves the stage-2 cost of every ordinary shake; a shake that
\\ actually pruned something re-runs the check in full.
(define ygg.init-order-recheck
  Tops Kept _ Status -> Status  where (= Tops Kept)
  _ Kept UserKL _ -> (ygg.init-order-check Kept UserKL))

\\ The user files' toplevel (non-defun) forms, in manifest order.
(define ygg.user-tops
  [] -> []
  [Forms | Files] -> (append (toplevel-forms Forms) (ygg.user-tops Files)))

\\ The lowest-indexed readBeforeWrite tuple, as [N V], or [] when there is
\\ none.  Datalog derives a set; the sentinel names one form, and which one
\\ has to be deterministic, so it is the first form that cannot run.
(define ygg.io-least
  [] Best -> Best
  [[_ N V] | Ts] [] -> (ygg.io-least Ts [N V])
  [[_ N V] | Ts] [M _] -> (ygg.io-least Ts [N V])  where (< N M)
  [_ | Ts] Best -> (ygg.io-least Ts Best))

(define ygg.io-report
  [] -> done
  [N V] -> (do (pr (make-string "yggdrasil-shake: FAIL init-order form=~A reads=~A~%"
                                N V)
                   (stoutput))
               (simple-error
                (make-string "init-order: form ~A reads ~A before any form sets it"
                             N V))))

\\ ---------------------------- the manifest key --------------------------
\\ `checked` only when the direct half of the rule discharged every read on
\\ its own: reads(N,V) with some writes(M,V), M < N, or V a port global.
\\ Otherwise one of the three widenings - the call graph, the form's own
\\ writes, a write inside a freeze - did some of the work, the answer is
\\ weaker than it looks, and the manifest says so.
\\
\\ That distinction is a relation, not a second implementation of the check:
\\ `weakRead(N,V)` is derived by the same rule set, in the same stratum as
\\ readBeforeWrite, from the same EDB - reads that `covered` covers but
\\ `directWrittenBefore` does not.  Scanning the reads/writes rows again in
\\ Shen would give the manifest key a private notion of "M < N" that nothing
\\ checks against the rules; this way the key's basis is in all three
\\ engines, and the oracle test diffs it like any other relation.
\\
\\ Read out of the database the check has just closed, so it is only ever
\\ called from ygg.init-order-check, after ygg.io-report has passed.
(define ygg.io-status
  -> (if (empty? (ygg.dl-query weakRead)) checked checked-weak))

\\ ------------------------------- the EDB --------------------------------
\\ reads/writes come from ygg.io-rows, which is also what the fact dump
\\ writes, so the engine here and the oracle there see the same tuples.
\\ ygg.io-facts (in the fact-dump section) dumps the five relations below
\\ from these same extractors, for the same reason.

(define ygg.io-edb
  Forms Rows Defuns
   -> (let Names (map (fn ygg.io-defun-name) Defuns)
           (append Rows
           (append (map (/. R [defwrite | R]) (ygg.io-defwrite-rows Defuns))
           (append (map (/. R [fcall | R])    (ygg.io-fcall-rows Defuns Names))
           (append (map (/. R [formcalls | R]) (ygg.io-formcall-rows Forms 1 Names))
           (append (map (/. R [formwrite | R]) (ygg.io-formwrite-rows Forms 1))
           (append (map (/. R [succ | R])     (ygg.io-succ-rows (ygg.len Forms)))
                   (map (/. V [portGlobal V]) (value *global-primitives*))))))))))

\\ The user files' defuns as [Name Body] pairs, in manifest order.
(define ygg.io-defuns
  [] -> []
  [Forms | Files] -> (append (ygg.io-file-defuns Forms) (ygg.io-defuns Files)))

(define ygg.io-file-defuns
  [] -> []
  [[defun F _ Body] | Fs] -> [[F Body] | (ygg.io-file-defuns Fs)]
  [_ | Fs] -> (ygg.io-file-defuns Fs))

(define ygg.io-defun-name
  [F _] -> F)

\\ (set V _) with a literal V anywhere in a DEFUN body - unlike
\\ ygg.io-writes this DOES descend into lambda and freeze, because a body's
\\ writes all happen when the defun is applied, however deeply they are
\\ wrapped.  (ygg.io-writes' exclusions are right for a toplevel form and
\\ wrong here, which is why this is its own walk.)  The sibling for reads is
\\ ygg.value-reads.
(define ygg.io-defun-writes
  [set V Val] -> [V | (ygg.io-defun-writes Val)]  where (ygg.cn-literal-sym? V)
  [X | Y] -> (append (ygg.io-defun-writes X) (ygg.io-defun-writes Y))
  _ -> [])

(define ygg.io-defwrite-rows
  [] -> []
  [[F Body] | Ds] -> (append (map (/. V [F V])
                                  (ygg.remove-dups (ygg.io-defun-writes Body)))
                             (ygg.io-defwrite-rows Ds)))

\\ graph-rows' counterpart for user code.  called-fns cannot be reused: it
\\ filters its symbols through kernel-defun?, which is a build-time mark on
\\ the KERNEL's defun names, so over user code it returns only the kernel
\\ names and never the user's own.  function-calls is the unfiltered walk;
\\ restricting it to the user defun names is the same restriction
\\ called-fns makes, against the other name set.
\\
\\ Both are symbol walks, so fcall/formcalls mean MENTIONS, not applies:
\\ (set *names* [setup]) counts as a call to setup.  That is the same
\\ precision graph-rows has for the kernel, and it errs the safe way for
\\ this rule - an extra edge can only discharge a read the check would
\\ otherwise refuse, never manufacture a violation - but it is an
\\ over-approximation on top of D13's, and analysis.dl records it as D14.
(define ygg.io-fcall-rows
  [] _ -> []
  [[F Body] | Ds] Names
   -> (append (map (/. G [F G])
                   (ygg.filter (/. G (element? G Names))
                               (ygg.remove-dups (function-calls Body))))
              (ygg.io-fcall-rows Ds Names)))

(define ygg.io-formcall-rows
  [] _ _ -> []
  [F | Fs] N Names
   -> (append (map (/. G [N G])
                   (ygg.filter (/. G (element? G Names))
                               (ygg.remove-dups (function-calls F))))
              (ygg.io-formcall-rows Fs (+ N 1) Names)))

\\ (set V _) with a literal V anywhere in a toplevel form, INCLUDING inside
\\ a lambda or a freeze, which is where ygg.io-writes stops.  ygg.io-writes
\\ answers "what does this form certainly write", which is the question
\\ stage 4's deadInit asks; formwrite answers "what could this form have
\\ written by the time it returns", which is the question a read that comes
\\ after it asks.  (thaw (freeze (set *f* 1))) is the difference.  A defun
\\ is not a toplevel form here, but the exclusion is written out anyway so
\\ that the walk cannot pick up a defun body if one is ever nested.
(define ygg.io-form-writes
  [defun _ _ _] -> []
  [set V Val] -> [V | (ygg.io-form-writes Val)]  where (ygg.cn-literal-sym? V)
  [X | Y] -> (append (ygg.io-form-writes X) (ygg.io-form-writes Y))
  _ -> [])

(define ygg.io-formwrite-rows
  [] _ -> []
  [F | Fs] N -> (append (map (/. V [N V])
                             (ygg.remove-dups (ygg.io-form-writes F)))
                        (ygg.io-formwrite-rows Fs (+ N 1))))

\\ succ(M,M+1) for every adjacent pair of toplevel forms: the order as an
\\ EDB relation, which is how the rules get a `<` out of an engine that has
\\ no arithmetic.  LINEAR on purpose - writtenBefore walks it transitively.
\\ The quadratic spelling (a tuple per M < N pair) derives exactly the same
\\ relation and was what this check first shipped with; 201 tuples instead
\\ of 20,301 for tests/metaeval.shen, and loading the big one is cubic in
\\ the form count, since ygg.dl-add dedupes each incoming tuple against its
\\ first-argument bucket.
(define ygg.io-succ-rows
  Len -> (ygg.io-succ-h 1 Len))

(define ygg.io-succ-h
  M Len -> []  where (>= M Len)
  M Len -> [[M (+ M 1)] | (ygg.io-succ-h (+ M 1) Len)])

\\ The five stage-2 relations as .facts files, for Souffle and refeval.py.
\\ reads/writes are dumped beside them by yggdrasil.facts from the same
\\ ygg.io-rows the check uses.
(define ygg.io-facts
  Dir Forms UserKL
   -> (let Defuns (ygg.io-defuns UserKL)
           Names  (map (fn ygg.io-defun-name) Defuns)
           F1 (ygg.facts-file Dir "defwrite"  (ygg.io-defwrite-rows Defuns))
           F2 (ygg.facts-file Dir "fcall"     (ygg.io-fcall-rows Defuns Names))
           F3 (ygg.facts-file Dir "formcalls" (ygg.io-formcall-rows Forms 1 Names))
           F4 (ygg.facts-file Dir "formwrite" (ygg.io-formwrite-rows Forms 1))
           F5 (ygg.facts-file Dir "succ"      (ygg.io-succ-rows (ygg.len Forms)))
           done))

\\ ------------------------------ the rules -------------------------------
\\ analysis/analysis.dl, "initialisation order (stage 2)", as Shen data -
\\ same relation names, same clause order, logic variables written as the
\\ lowercase names ygg.*dl-vars* maps (see (value *shake-rules*)).
\\
\\   wantedGlobal(V)         :- reads(_,V).
\\   writesVia(F,V)          :- defwrite(F,V).
\\   writesVia(F,V)          :- fcall(F,G), writesVia(G,V).
\\   topWrites(N,V)          :- formwrite(N,V).
\\   topWrites(N,V)          :- formcalls(N,G), writesVia(G,V).
\\   writtenBefore(N,V)      :- topWrites(M,V), succ(M,N), wantedGlobal(V).
\\   writtenBefore(N,V)      :- writtenBefore(M,V), succ(M,N).
\\   directWrittenBefore(N,V):- writes(M,V), succ(M,N), wantedGlobal(V).
\\   directWrittenBefore(N,V):- directWrittenBefore(M,V), succ(M,N).
\\   covered(N,V)            :- writtenBefore(N,V).
\\   covered(N,V)            :- topWrites(N,V), wantedGlobal(V).
\\   readBeforeWrite(N,V)    :- reads(N,V), !covered(N,V), !portGlobal(V).
\\   weakRead(N,V)           :- reads(N,V), covered(N,V),
\\                              !directWrittenBefore(N,V), !portGlobal(V).
\\
\\ Four strata, and the order is forced: covered and directWrittenBefore
\\ must both be closed before the last stratum negates them, or
\\ ygg.dl-check-neg would (rightly) refuse the rule set as unstratified.
\\ readBeforeWrite decides the shake; weakRead decides the manifest key.
\\ The two `succ` recursions are what make the linear order EDB enough;
\\ `covered` is writtenBefore plus the form's own writes, which is the
\\ same-form case, and it is deliberately NOT part of directWrittenBefore,
\\ so a read discharged by its own form reads checked-weak.
\\
\\ `wantedGlobal` is the demand set, and it is in the rules rather than in
\\ the extractors so that all three engines derive the same tuples.  Only a
\\ global some toplevel form READS can appear in readBeforeWrite or
\\ weakRead, and the two order closures are the only expensive relations
\\ here: unrestricted they are (globals written) x (forms after the write),
\\ which on tests/metaeval.shen is ~14,000 tuples derived to answer a
\\ question about that program's single toplevel read.  Restricted it is
\\ ~200, and the whole check costs about 0.02s there instead of 0.55s.
(set ygg.*init-order-rules*
  (ygg.dl-varify
   [[[[wantedGlobal v] [reads n v]]
     [[writesVia f v] [defwrite f v]]
     [[writesVia f v] [fcall f g] [writesVia g v]]]
    [[[topWrites n v] [formwrite n v]]
     [[topWrites n v] [formcalls n g] [writesVia g v]]]
    [[[writtenBefore n v] [topWrites m v] [succ m n] [wantedGlobal v]]
     [[writtenBefore n v] [writtenBefore m v] [succ m n]]
     [[directWrittenBefore n v] [writes m v] [succ m n] [wantedGlobal v]]
     [[directWrittenBefore n v] [directWrittenBefore m v] [succ m n]]
     [[covered n v] [topWrites n v] [wantedGlobal v]]
     [[covered n v] [writtenBefore n v]]]
    [[[readBeforeWrite n v] [reads n v]
                            [not [covered n v]]
                            [not [portGlobal v]]]
     [[weakRead n v] [reads n v]
                     [covered n v]
                     [not [directWrittenBefore n v]]
                     [not [portGlobal v]]]]]))

\\ (value V) / (set V _) occurrences in a form's own expression, at any
\\ depth (let, do, if, ...), but never inside a defun, a lambda or a
\\ freeze: those bodies run when they are applied or forced, not while the
\\ form itself is evaluated.  Skipping lambdas also keeps the eta-wrapper
\\ literal trim-top builds for shen.*lambdatable* out of the scan - its
\\ entry for the `value` primitive is literally (lambda X1 (value X1)).
\\
\\ The guard is ygg.cn-literal-sym?, not symbol?: a KL variable is a symbol
\\ too, so symbol? alone reports (value X) inside (let X *a* ...) as a read
\\ of the global "X".  A (value X) / (set X _) whose X is a variable is a
\\ COMPUTED name - stage 3's ygg.cn-global? and the computed-names= manifest
\\ key are what say so - and stage 2 has nothing true to say about it.
(define ygg.io-reads
  [defun | _] -> []
  [lambda | _] -> []
  [freeze | _] -> []
  [value V] -> [V]  where (ygg.cn-literal-sym? V)
  [X | Y] -> (append (ygg.io-reads X) (ygg.io-reads Y))
  _ -> [])

(define ygg.io-writes
  [defun | _] -> []
  [lambda | _] -> []
  [freeze | _] -> []
  [set V Val] -> [V | (ygg.io-writes Val)]  where (ygg.cn-literal-sym? V)
  [X | Y] -> (append (ygg.io-writes X) (ygg.io-writes Y))
  _ -> [])

\\ ===================== stage 4: dead initialisation =====================
\\ docs/analysis-rules.md stage 4.  The synthesised initialiser writes ~35
\\ globals; most of them belong to machinery the shake has already thrown
\\ away.  A global is LIVE when
\\   - a reachable kernel defun evaluates (value V)          readsIn + reach
\\   - a kept toplevel form evaluates (value V)              reads
\\   - the port's runtime reads it natively                  portReads
\\   - its name occurs anywhere in the user KL               rawsym
\\ and DEAD otherwise, at which point the form that writes it can be dropped
\\ from the initialiser.  The last clause is an over-approximation in the
\\ shake's own symbol-based style: a user program that says *hush* anywhere,
\\ in any position, keeps *hush*.
\\
\\ Only the simplest forms are ever dropped: a form must be exactly
\\ (set V Lit) with Lit an atom (a number, a string, a boolean, a symbol or
\\ ()).  A form whose value is a call - (set *property-vector* (vector 20000)),
\\ (set shen.*special* (cons @p ...)) - is never pruned even when its global
\\ is dead, because evaluating it is an effect in its own right and dropping
\\ it would change what the artifact does, not just what it remembers.  The
\\ same goes for a form that is not a `set` at all.
\\
\\ portReads is per-builder data (builders.json, "port_reads"), because it is
\\ a fact about a port's runtime and not about Shen.  The default below is the
\\ conservative union; the Go driver overrides it per shake from builders.json
\\ (the union over all targets for `shake`, the target's own list for
\\ `build`/`run --target T`).
\\
\\ Off by default: (value ygg.*prune-init*) is false, and with it false the
\\ emitted kernel.kl is byte-identical to a pre-stage-4 shake's.  The parity
\\ gate decides per target whether it is safe to turn on.

(set ygg.*prune-init* false)

\\ Default: the UNION of the port_reads lists that have actually been read
\\ off a runtime -- today shen-go's five, since `go` is the only entry in
\\ builders.json that declares one.  Every other target's entry declares
\\ none and inherits `_default`, whose value is the literal string
\\ "unknown": nobody has measured what those runtimes read natively.
\\
\\ This list is a COPY of that union, and builders.json is the authority.
\\ It exists for a direct host invocation -- (load "yggdrasil.shen") and
\\ call ygg.shake yourself -- where there is no Go driver to push a list
\\ in.  Every invocation through `yggdrasil` or Bifrost overrides it per
\\ shake (prune.go's portReadsFor: the target's own declared list, else the
\\ same union, and --prune-init REFUSES a target that resolves to unknown).
\\ The two must stay equal element for element; TestPortReadsDefaultMatchesShen
\\ in prune_test.go computes the union from builders.json, parses this form,
\\ and fails if they drift.
\\
\\ It is NOT a conservative superset for a port nobody has measured, and it
\\ no longer pretends to be: it held 35 names for exactly that reason --
\\ go's five plus every global the kernel's own defuns read in a full boot
\\ -- and that guess was indistinguishable, to everything downstream, from
\\ a list somebody had checked.  If you set ygg.*prune-init* by hand on a
\\ host, you are pruning against shen-go's reads, whatever you then build.
(set ygg.*port-reads*
     [\\ read natively by the go runtime (shen-go, kl/): open / load-file
      \\ via ResolveHomePath, arity and fn via kernelArity / nativeFn,
      \\ then the two port globals.  Sorted, as portReadsFor returns them.
      *home-directory* *property-vector* *stinput* *stoutput*
      shen.*lambdatable*])

\\ (value V) with a literal V anywhere in a defun's body - unlike
\\ ygg.io-reads this DOES descend into lambda and freeze, because a defun
\\ body's reads all happen when the defun is called, however deeply they are
\\ wrapped.
(define ygg.value-reads
  [value V] -> [V]  where (and (symbol? V) (not (variable? V)))
  [X | Y] -> (append (ygg.value-reads X) (ygg.value-reads Y))
  _ -> [])

(define ygg.readsin-rows
  [] -> []
  [[defun F _ Body] | Code] -> (append (map (/. V [F V]) (ygg.value-reads Body))
                                       (ygg.readsin-rows Code))
  [_ | Code] -> (ygg.readsin-rows Code))

\\ reads/writes over the FINAL form sequence, indexed exactly as
\\ ygg.init-order-check indexes it: the kept kernel init forms first, then
\\ the user files' toplevel forms.  Same extractors, so the two checks
\\ cannot drift.
(define ygg.io-rows
  Forms -> (ygg.io-rows-h Forms 1))

(define ygg.io-rows-h
  [] _ -> []
  [F | Fs] N -> (append (append (map (/. V [reads N V]) (ygg.io-reads F))
                                (map (/. V [writes N V]) (ygg.io-writes F)))
                        (ygg.io-rows-h Fs (+ N 1))))

(define ygg.init-edb
  Kernel Reach Forms RawFs
   -> (append (map (/. F [reach F]) Reach)
      (append (map (/. R [readsIn | R]) (ygg.readsin-rows Kernel))
      (append (ygg.io-rows Forms)
      (append (map (/. V [portReads V]) (value ygg.*port-reads*))
              (map (/. S [rawsym S]) (ygg.remove-dups RawFs)))))))

(set ygg.*init-rules*
  (ygg.dl-varify
   [[[[liveGlobal v] [readsIn f v] [reach f]]
     [[liveGlobal v] [reads n v]]
     [[liveGlobal v] [portReads v]]
     [[liveGlobal v] [writes n v] [rawsym v]]]
    [[[deadInit n v] [writes n v] [not [liveGlobal v]]]]]))

\\ The form indices with a deadInit tuple.  Runs the engine a SECOND time,
\\ with its own EDB: `reads`/`writes` are taken from the forms trim-top
\\ actually left, which depend on the footprint, which is what the first run
\\ computed - so reach is handed to this run as a fact rather than re-derived.
(define ygg.dead-init
  _ _ _ _ _ -> []  where (not (value ygg.*prune-init*))
  Kernel Reach Tops UserKL RawFs
   -> (let Forms (append Tops (ygg.user-tops UserKL))
           Run   (ygg.dl-run (ygg.init-edb Kernel Reach Forms RawFs)
                             (value ygg.*init-rules*))
           (ygg.remove-dups (map (/. T (hd (tl T))) (ygg.dl-query deadInit)))))

(define ygg.prune-init
  Tops _ -> Tops  where (not (value ygg.*prune-init*))
  Tops Dead -> (ygg.prune-h Tops 1 Dead))

(define ygg.prune-h
  [] _ _ -> []
  [T | Ts] N Dead -> (ygg.prune-h Ts (+ N 1) Dead)  where (ygg.prunable? T N Dead)
  [T | Ts] N Dead -> [T | (ygg.prune-h Ts (+ N 1) Dead)])

(define ygg.prunable?
  [set V Lit] N Dead -> (and (element? N Dead) (ygg.init-literal? Lit))
      where (symbol? V)
  _ _ _ -> false)

(define ygg.init-literal?
  [] -> true
  [_ | _] -> false
  _ -> true)

\\ ======================= footprint attribution (why) ====================
\\ The device Tarver asked for on the Shen group (The Future of Shen,
\\ 2026-09): something that scans a program and highlights the parts that
\\ drag kernel in - "code creep", his example being a partial function
\\ pulling the whole tracker in through shen.f-error.  The shake already
\\ knows the answer (the footprint is a reachability set over a graph it
\\ owns); this reports it instead of just emitting it.
\\
\\   (yggdrasil.why ["prog.shen"])            attribution report
\\   (yggdrasil.why-trace ["prog.shen"] F)    ... plus the shortest call
\\                                            chain from the program to
\\                                            kernel function F
\\
\\ Numbers are all in kernel defuns.  The FLOOR is what the kernel's own
\\ toplevel init forms reach with no user code at all - every program pays
\\ it.  For each user defun (and the file's toplevel forms as one row) and
\\ for each kernel function the user KL mentions directly (a "seed"):
\\   adds       = |reach(floor-seeds + this)| - |floor|
\\                what this costs on top of the floor, alone
\\   exclusive  = |total| - |reach(everything except this)|
\\                what disappears from kernel.kl if this went away
\\ adds > exclusive means the cost is shared with other rows.  Rows print
\\ sorted by adds, descending; a row that adds nothing is still listed so
\\ the report is a complete map of the program.
\\
\\ It runs under the same mode the shake would (eval-free stripping on or
\\ off, decided by the same eval-free? test), so the report is about the
\\ kernel.kl that shake would actually write.  Compare an eval-free and an
\\ eval-capable program to see the stripping at work: shen.f-error is a
\\ 1-defun row in the first and a ~300-defun row in the second.
\\
\\ Output contract (the Go driver prints from the first sentinel line on):
\\   yggdrasil-why: mode=M floor=N total=N kernel=N
\\   eval-capable because the program mentions: F G   (eval-capable only)
\\   defun NAME adds=N exclusive=N
\\   top NAME adds=N exclusive=N
\\   seed NAME adds=N exclusive=N
\\   trace F: A -> B -> F           (or "trace F: not in footprint")

(define yggdrasil.why
  Files -> (ygg.why-h Files []))

(define yggdrasil.why-trace
  Files Target -> (ygg.why-h Files [Target]))

(define ygg.why-h
  Files Targets
   -> (let MaxPrint  (value *maximum-print-sequence-size*)
           Unlimit   (set *maximum-print-sequence-size* 1000000000)
           Kernel    (kernel-code)
           Graph     (call-graph Kernel)
           KLFiles   (map (fn bootstrap) Files)
           RawKL     (map (fn read-file) KLFiles)
           RawFs     (function-calls RawKL)
           EvalFree  (eval-free? RawFs)
           EvalBy    (ygg.filter (/. F (element? F (value *eval-entry-points*))) RawFs)
           KL        (strip-user-declares RawKL EvalFree)
           Tops      (prepare-tops (toplevel-forms Kernel) EvalFree)
           Graph2    (if EvalFree (strip-f-error-row Graph) Graph)
           InitSeeds (ygg.remove-dups (mapcan (fn called-fns) Tops))
           UserSeeds (ygg.kernel-seeds (function-calls KL))
           Floor     (footprint InitSeeds Graph2)
           Total     (footprint (append InitSeeds UserSeeds) Graph2)
           Rows      (ygg.why-rows KL InitSeeds UserSeeds Floor Total Graph2)
           Header    (pr (make-string "yggdrasil-why: mode=~A floor=~A total=~A kernel=~A~%"
                                      (if EvalFree "eval-free" "eval-capable")
                                      (ygg.len Floor) (ygg.len Total)
                                      (ygg.len Graph))
                         (stoutput))
           Cause     (if EvalFree done
                         (pr (make-string "eval-capable because the program mentions:~A~%"
                                          (ygg.chain-sp EvalBy))
                             (stoutput)))
           Print     (ygg.mapc (fn ygg.pr-why-row) (ygg.sort-desc Rows))
           Traces    (ygg.mapc (/. T (ygg.pr-trace T UserSeeds InitSeeds Graph2))
                               Targets)
           Restore   (set *maximum-print-sequence-size* MaxPrint)
           done))

(define ygg.kernel-seeds
  Fs -> (ygg.filter (fn kernel-defun?) (ygg.remove-dups Fs)))

\\ One row per user defun, one per file's toplevel forms, one per kernel
\\ seed.  Each row is [Kind Name Adds Exclusive]; Adds and Exclusive are
\\ two footprint traversals, O(V+E) each, so the report costs a few dozen
\\ shakes' worth of graph work - cheap.
(define ygg.why-rows
  KL Init Seeds Floor Total Graph
   -> (append (ygg.user-rows KL Init Seeds Floor Total Graph)
              (map (/. S (ygg.why-row seed S [S] Init Seeds Floor Total Graph))
                   Seeds)))

(define ygg.user-rows
  [] _ _ _ _ _ -> []
  [Forms | Files] Init Seeds Floor Total Graph
   -> (append (ygg.form-rows Forms [] Init Seeds Floor Total Graph)
              (ygg.user-rows Files Init Seeds Floor Total Graph)))

\\ Tops accumulates the non-defun forms of one file; they are reported as
\\ a single "top" row because they run in source order as one unit.
(define ygg.form-rows
  [] [] _ _ _ _ _ -> []
  [] Tops Init Seeds Floor Total Graph
   -> [(ygg.why-row top toplevel (ygg.kernel-seeds (function-calls Tops))
                    Init Seeds Floor Total Graph)]
  [[defun F _ Body] | Forms] Tops Init Seeds Floor Total Graph
   -> [(ygg.why-row defun F (ygg.kernel-seeds (function-calls Body))
                    Init Seeds Floor Total Graph)
       | (ygg.form-rows Forms Tops Init Seeds Floor Total Graph)]
  [Form | Forms] Tops Init Seeds Floor Total Graph
   -> (ygg.form-rows Forms [Form | Tops] Init Seeds Floor Total Graph))

(define ygg.why-row
  Kind Name Mine Init Seeds Floor Total Graph
   -> (let Alone  (footprint (append Init Mine) Graph)
           Others (ygg.filter (/. S (not (element? S Mine))) Seeds)
           Rest   (footprint (append Init Others) Graph)
           [Kind Name (- (ygg.len Alone) (ygg.len Floor))
                      (- (ygg.len Total) (ygg.len Rest))]))

(define ygg.pr-why-row
  [Kind Name Adds Excl] -> (pr (make-string "~A ~A adds=~A exclusive=~A~%"
                                            Kind Name Adds Excl)
                               (stoutput)))

\\ Insertion sort on the Adds field, descending, stable.
(define ygg.sort-desc
  [] -> []
  [R | Rs] -> (ygg.insert-desc R (ygg.sort-desc Rs)))

(define ygg.insert-desc
  R [] -> [R]
  R [S | Ss] -> [R S | Ss]  where (>= (ygg.row-adds R) (ygg.row-adds S))
  R [S | Ss] -> [S | (ygg.insert-desc R Ss)])

(define ygg.row-adds
  [_ _ Adds _] -> Adds)

\\ Shortest chain to Target, breadth-first over the same graph the shake
\\ uses.  User seeds are searched first so the chain starts at something
\\ the program wrote; the init forms are tried only if the user code
\\ cannot reach Target on its own.
(define ygg.pr-trace
  Target UserSeeds InitSeeds Graph
   -> (let Path (ygg.first-path Target [UserSeeds InitSeeds] Graph)
           (pr (if (empty? Path)
                   (make-string "trace ~A: not in footprint~%" Target)
                   (make-string "trace ~A: ~A~%" Target (ygg.chain Path)))
               (stoutput))))

(define ygg.first-path
  _ [] _ -> []
  Target [Seeds | More] Graph
   -> (let Path (ygg.path Target (map (/. S [S]) Seeds) [] Graph)
           (if (empty? Path) (ygg.first-path Target More Graph) Path)))

\\ Queue entries are reversed paths, head = the node to expand.
(define ygg.path
  _ [] _ _ -> []
  Target [[Target | Path] | _] _ _ -> (reverse [Target | Path])
  Target [[N | _] | Q] Seen Graph -> (ygg.path Target Q Seen Graph)
      where (element? N Seen)
  Target [[N | Path] | Q] Seen Graph
   -> (ygg.path Target
                (append Q (map (/. C [C N | Path]) (row-calls N Graph)))
                [N | Seen] Graph))

(define ygg.chain
  [F] -> (str F)
  [F | Fs] -> (cn (str F) (cn " -> " (ygg.chain Fs))))

(define ygg.chain-sp
  [] -> ""
  [F | Fs] -> (cn " " (cn (str F) (ygg.chain-sp Fs))))

\\ ===================== fact dump for the Datalog oracle =================
\\ (yggdrasil.facts ["prog.shen"] "dir") writes the shake's decisions out as
\\ TSV fact files, one per relation in analysis/analysis.dl, so an external
\\ Datalog engine (Souffle in CI, analysis/refeval.py locally) can recompute
\\ the footprint from the rules and be diffed against the kernel.kl this
\\ same pipeline would write.  See docs/analysis-rules.md, stage 1.
\\
\\ It is a SIBLING of yggdrasil.shake, not a hook inside it: the shake's
\\ behaviour, and every byte it emits, is untouched.  The pipeline below is
\\ shake's, verbatim, up to the footprint; everything after that is
\\ extraction.  Nothing here is called during a normal shake.
\\
\\ Extraction is deliberately mode-agnostic where it can be.  The kernel's
\\ own edge facts (callpos/argpos/datasym) are taken from the RAW kernel, and
\\ each toplevel init form is dumped twice - raw (formmentions) and as
\\ prepare-tops would leave it for an eval-free program (formmentionsef) -
\\ so the eval-free/eval-capable choice is made by the RULES, from
\\ entry + rawsym, exactly as eval-free? makes it here.  The two relations
\\ that cannot be mode-agnostic are initprim (trim-top's output depends on
\\ the footprint, which depends on the mode) and usersym (strip-user-declares
\\ is mode-gated); both are dumped for the mode this program is actually in,
\\ with rawsym carrying the unstripped user symbols alongside.

(define yggdrasil.facts
  Files Dir
   -> (let P        (ygg.pipeline Files)
           Kernel   (ygg.pl-kernel P)
           Graph    (ygg.pl-graph P)
           RawFs    (ygg.pl-rawfs P)
           EvalFree (ygg.pl-evalfree P)
           KL       (ygg.pl-kl P)
           AllTops  (ygg.pl-alltops P)
           Tops     (ygg.pl-tops P)
           Foot     (ygg.pl-foot P)
           Arities  (arity-literal Tops)
           TopsOut  (map (/. T (trim-top T Foot EvalFree Arities)) Tops)
           Cls      (ygg.cls-defuns Kernel)
           W1  (ygg.facts-file Dir "kernel"   (map (/. R [(row-head R)]) Graph))
           W2  (ygg.facts-file Dir "callpos"  (ygg.rows-of callpos Cls))
           W3  (ygg.facts-file Dir "argpos"   (ygg.rows-of argpos Cls))
           W4  (ygg.facts-file Dir "datasym"  (ygg.rows-of datasym Cls))
           W5  (ygg.facts-file Dir "mentionsprim" (ygg.prim-rows Kernel))
           W6  (ygg.facts-file Dir "top"      (ygg.top-rows AllTops 1))
           W7  (ygg.facts-file Dir "formmentions"   (ygg.mention-rows AllTops 1 false))
           W8  (ygg.facts-file Dir "formmentionsef" (ygg.mention-rows AllTops 1 true))
           W9  (ygg.facts-file Dir "rawsym"   (map (/. S [S]) (ygg.remove-dups RawFs)))
           W10 (ygg.facts-file Dir "usersym"  (map (/. S [S])
                                                   (ygg.remove-dups (function-calls KL))))
           W11 (ygg.facts-file Dir "entry"    (map (/. S [S]) (value *eval-entry-points*)))
           W12 (ygg.facts-file Dir "prim"     (map (/. P [P]) (value *primitives*)))
           W13 (ygg.facts-file Dir "cap"      (mapcan (fn ygg.cap-rows) (value *capabilities*)))
           W14 (ygg.facts-file Dir "portGlobal" (map (/. V [V]) (value *global-primitives*)))
           W15 (ygg.facts-file Dir "initprim" (map (/. P [P]) (find-primitives TopsOut)))
           W16 (ygg.facts-file Dir "userintern" (ygg.rows-of userintern (ygg.cn-facts KL)))
           W17 (ygg.facts-file Dir "userglobal" (ygg.rows-of userglobal (ygg.cn-facts KL)))
           Forms (append TopsOut (ygg.user-tops KL))
           W18 (ygg.facts-file Dir "readsIn"   (ygg.readsin-rows Kernel))
           W19 (ygg.facts-file Dir "reads"     (ygg.rows-of reads (ygg.io-rows Forms)))
           W20 (ygg.facts-file Dir "writes"    (ygg.rows-of writes (ygg.io-rows Forms)))
           W21 (ygg.facts-file Dir "portReads" (map (/. V [V]) (value ygg.*port-reads*)))
           \\ Initialisation order (stage 2).  Same four extractors the
           \\ check's own EDB is built from, so the oracle evaluates
           \\ readBeforeWrite over exactly the tuples ygg.dl saw.
           WIO (ygg.io-facts Dir Forms KL)
           \\ Runtime trace (docs/analysis-rules.md, "Runtime trace").
           \\ uncoveredRead's other half, defunwrite, has no stage-4
           \\ counterpart - stage 4 only ever cares about TOPLEVEL writes -
           \\ so it is dumped here; its toplevel half is not, because that is
           \\ exactly the projection writes(_, V), and analysis.dl derives it.
           \\ called/readglobal are written EMPTY and overwritten by
           \\ `yggdrasil trace-check` from a real run, so that a facts dir is
           \\ always a complete .input set whether or not a trace was taken.
           W22 (ygg.facts-file Dir "defunwrite"
                                (map (/. V [V]) (ygg.trace-defunwrites Foot Kernel KL)))
           W23 (ygg.facts-file Dir "called" [])
           W24 (ygg.facts-file Dir "readglobal" [])
           Restore (ygg.pl-restore P)
           Report  (pr (make-string "yggdrasil-facts: mode=~A dir=~A kernel=~A~%"
                                    (if EvalFree "eval-free" "eval-capable")
                                    Dir (ygg.len Graph))
                       (stoutput))
           done))

(define ygg.cap-rows
  [C | Sinks] -> (map (/. P [C P]) Sinks))

\\ ---------------------------- edge facts --------------------------------
\\ ygg.cls is a structural mirror of called-fns: same clause order, same
\\ cons walk, same four data-table exceptions.  The only thing it adds is a
\\ position, so that the one set of symbols called-fns returns can be split
\\ into callpos (a cons's head cell) and argpos (any later cell, tagged with
\\ the head it sits under), and so that the symbols called-fns THROWS AWAY
\\ at an exception are still recorded, as datasym.  The split is for the
\\ rules' legibility only - analysis.dl derives an edge from callpos and
\\ argpos alike, because called-fns does (see the deviation notes there).
\\
\\ Position is one of: form (this node is an expression), args (a cons that
\\ is the tail of an argument list), arg (one argument), call (a head cell).
\\ Caller is the head symbol the current argument list belongs to.
\\
\\ The exception clauses come first, as they do in called-fns, so that they
\\ fire in argument-list tails too - which is where the kernel's own
\\ (put P shen.external-symbols ...) and (declare F T) forms actually sit.

(define ygg.cls
  [shen.initialise-arity-table Lit] _ _
      -> [[callpos shen.initialise-arity-table] | (ygg.data Lit)]
  [declare F Ty] _ _ -> (append (ygg.cls-sym declare) (ygg.data [F Ty]))
      where (symbol? F)
  [put P shen.external-symbols Lit | Rest] _ _
      -> (append (ygg.cls-sym put)
                 (append (ygg.data [P shen.external-symbols Lit])
                         (ygg.cls Rest form put)))
      where (symbol? P)
  [set shen.*special* Lit] _ _
      -> (append (ygg.cls-sym set) (ygg.data [shen.*special* Lit]))
  [set shen.*extraspecial* Lit] _ _
      -> (append (ygg.cls-sym set) (ygg.data [shen.*extraspecial* Lit]))
  [shen.assoc-> K | R] _ _
      -> (append (ygg.cls-sym shen.assoc->)
                 (append (ygg.data [K]) (ygg.cls R form shen.assoc->)))
      where (symbol? K)
  [X | Y] form _      -> (append (ygg.cls X call (ygg.caller X))
                                 (ygg.cls Y args (ygg.caller X)))
  [X | Y] args Caller -> (append (ygg.cls X arg Caller) (ygg.cls Y args Caller))
  [X | Y] arg Caller  -> (ygg.cls [X | Y] form Caller)
  [X | Y] call Caller -> (ygg.cls [X | Y] form Caller)
  F call _ -> [[callpos F]] where (and (symbol? F) (kernel-defun? F))
  F form _ -> [[callpos F]] where (and (symbol? F) (kernel-defun? F))
  F _ Caller -> [[argpos F Caller]] where (and (symbol? F) (kernel-defun? F))
  _ _ _ -> [])

\\ A cons in head position - ((lambda X ...) Y) and friends - has no name
\\ to attribute its arguments to; argpos's third column says so rather than
\\ carrying a whole form.
(define ygg.caller
  X -> X where (symbol? X)
  _ -> ygg.nonsymbolic-head)

(define ygg.cls-sym
  F -> [[callpos F]] where (and (symbol? F) (kernel-defun? F))
  _ -> [])

\\ Every kernel name inside a subtree called-fns discards at an exception.
(define ygg.data
  [X | Y] -> (append (ygg.data X) (ygg.data Y))
  F -> [[datasym F]] where (and (symbol? F) (kernel-defun? F))
  _ -> [])

(define ygg.cls-defuns
  [] -> []
  [[defun F _ Body] | Code] -> (append (ygg.tag-with F (ygg.cls Body form F))
                                       (ygg.cls-defuns Code))
  [_ | Code] -> (ygg.cls-defuns Code))

(define ygg.tag-with
  _ [] -> []
  F [[Tag | Cols] | Ts] -> [[Tag F | Cols] | (ygg.tag-with F Ts)])

(define ygg.rows-of
  _ [] -> []
  Tag [[Tag | Cols] | Ts] -> [Cols | (ygg.rows-of Tag Ts)]
  Tag [_ | Ts] -> (ygg.rows-of Tag Ts))

\\ Primitives per kernel defun, so the rules can compute the manifest's
\\ primitive set over the reachable defuns rather than being handed it.
(define ygg.prim-rows
  [] -> []
  [[defun F _ Body] | Code] -> (append (map (/. P [F P]) (find-primitives Body))
                                       (ygg.prim-rows Code))
  [_ | Code] -> (ygg.prim-rows Code))

\\ ------------------------- toplevel init forms --------------------------
\\ Indices are over the RAW toplevel forms, so the two mention relations
\\ line up form for form.  prepare-tops in eval-free mode drops declare
\\ forms outright (no formmentionsef row at all) and rewrites the *macros*
\\ registration and the lambda-table builder (strip-eval-top), which is why
\\ the ef row of those two forms is shorter than the raw one.

(define ygg.top-rows
  [] _ -> []
  [T | Ts] N -> [[N (ygg.top-name T)] | (ygg.top-rows Ts (+ N 1))])

(define ygg.top-name
  [F | _] -> F where (symbol? F)
  _ -> top)

(define ygg.mention-rows
  [] _ _ -> []
  [T | Ts] N true -> (ygg.mention-rows Ts (+ N 1) true) where (declare-form? T)
  [T | Ts] N true -> (append (map (/. G [N G]) (called-fns (strip-eval-top T)))
                             (ygg.mention-rows Ts (+ N 1) true))
  [T | Ts] N false -> (append (map (/. G [N G]) (called-fns T))
                              (ygg.mention-rows Ts (+ N 1) false)))

\\ ------------------------------ TSV writer ------------------------------
\\ Tab-separated, newline-terminated, no header: Souffle's default .input
\\ format, and trivial for the Python reference evaluator to read.

(define ygg.facts-file
  Dir Name Rows -> (let Sink  (open (cn (cn Dir "/") (cn Name ".facts")) out)
                        Write (ygg.mapc (/. R (ygg.pr-fact R Sink)) Rows)
                        Close (close Sink)
                        done))

(define ygg.pr-fact
  Cols Sink -> (do (ygg.pr-cols Cols Sink true) (pr (n->string 10) Sink)))

(define ygg.pr-cols
  [] _ _ -> done
  [C | Cs] Sink true  -> (do (pr (ygg.fact-str C) Sink) (ygg.pr-cols Cs Sink false))
  [C | Cs] Sink false -> (do (pr (cn (n->string 9) (ygg.fact-str C)) Sink)
                             (ygg.pr-cols Cs Sink false)))

(define ygg.fact-str
  X -> X where (string? X)
  X -> (str X))

\\ ================= runtime call trace (--trace weaving) =================
\\ Stage-3 companion to the rule set (docs/analysis-rules.md, "Runtime
\\ trace").  The rules say which kernel defuns a program can reach; this
\\ says which ones a RUN actually entered, so the two can be compared.
\\
\\ It is aspect weaving, done at the KLambda IR rather than at a target's
\\ object code, which is what makes it target-independent: one weaver, and
\\ every stage-2 builder inherits it for free.
\\
\\   pointcut   every defun entry; every (value V) with a literal symbol V
\\   advice     append one line to a trace stream
\\   join model the woven form is still KL, so it compiles on every port
\\
\\ A defun (defun F Args Body) becomes (defun F Args (do (ygg.traced F)
\\ Body)), and (value V) becomes (ygg.traced-value V) - which records V and
\\ then performs the read, so the advice is around, not instead of.
\\ Weaving runs AFTER the footprint, the rewrites, trim-top and the
\\ init-order check, and just before anything is written, so it cannot
\\ perturb any decision the shake made: the traced kernel.kl has exactly
\\ the defuns of the untraced one, plus the six helpers.
\\
\\ The helpers must not be traced (infinite recursion: ygg.traced calling
\\ ygg.traced) and must not depend on the footprint, because the footprint
\\ was decided before they existed.  They therefore use only KL primitives
\\ - open, close, write-byte, string->n, pos, tlstr, str, value, set, plus
\\ the special forms if/let/do - and in particular NOT `pr`, which is a kernel
\\ defun (writer.kl) that reads *hush* and need not be in the footprint.
\\
\\ Default is off, and the whole of this section is inert then:
\\ ygg.trace-kernel and ygg.trace-user are the identity, and the manifest
\\ writers emit nothing, so an untraced shake is byte-identical.

(set ygg.*trace* false)
(set ygg.*trace-file* "yggdrasil.trace")

\\ The public traced entry point.  (yggdrasil.shake Files Dir) is unchanged.
(define yggdrasil.shake-traced
  Files Dir -> (let On  (set ygg.*trace* true)
                    R   (trap-error (yggdrasil.shake Files Dir)
                                    (/. E (ygg.trace-off-then E)))
                    Off (set ygg.*trace* false)
                    R))

(define ygg.trace-off-then
  E -> (do (set ygg.*trace* false) (simple-error (error-to-string E))))

\\ ---------------------------- the weaver --------------------------------

(define ygg.trace-kernel
  Code -> Code  where (not (value ygg.*trace*))
  Code -> (append (map (fn ygg.trace-defun) Code) (ygg.trace-helpers)))

\\ The LAST user file gains one extra toplevel form, (ygg.trace-end), after
\\ its own last form.  That is the end-of-run record and the close of the
\\ stream: the only thing a reader of the trace file can use to tell a
\\ complete run from one that died, or from a tail the port never flushed.
\\ A program that errors or exits before it gets there legitimately writes
\\ no end record, and the check says exactly that rather than blaming the
\\ containment rules.
(define ygg.trace-user
  Files -> Files  where (not (value ygg.*trace*))
  Files -> (ygg.trace-end-last
            (ygg.trace-phase-flip
             (map (/. Forms (map (fn ygg.trace-defun) Forms)) Files))))

(define ygg.trace-end-last
  [] -> []
  [Last] -> [(append Last [[ygg.trace-end]])]
  [F | Fs] -> [F | (ygg.trace-end-last Fs)])

\\ THE PHASE BOUNDARY: the user program's first non-defun toplevel form.
\\
\\ It used to be the last thing the woven shen.initialise did, and on a
\\ shaken artifact the two are the same point.  On an UNSHAKEN one they are
\\ not, and the difference is not small: installing the user's own defuns is
\\ done by the port, and a port with the whole kernel behind it installs them
\\ through the kernel's own arity table -- shen.store-arity,
\\ shen.execute-store-arity, shen.update-lambdatable, shen.lambda-function,
\\ shen.assoc-> and append.  On `yggdrasil trace-check --full tests/fib.shen
\\ --target go` that was the first 424 records of the "program" phase, all of
\\ them outside the slice's reach, and every one of them initialisation.  A
\\ containment check reading that as the program reaching outside its
\\ footprint is a check that fails for the wrong reason, which is only one
\\ step better than a check that cannot fail.
\\
\\ So the phase byte means: b = initialisation, which is the kernel's
\\ initialiser AND the loading of the user's definitions; p = the user's
\\ program proper, from its first toplevel form that is not a definition.  A
\\ program that is nothing but definitions never reaches such a form, so the
\\ flip is appended after the last file's last form instead -- it must
\\ execute, or the run reads as one whose program phase never started.
(define ygg.trace-phase-flip
  Files -> (let R (ygg.tpf-files Files)
             (if (hd R) (tl R) (ygg.tpf-append-last (tl R)))))

\\ Both helpers return [Flipped? | Rest]: the flip goes in at the first
\\ non-defun form of the first file that has one, and nowhere else.
(define ygg.tpf-files
  [] -> [false]
  [F | Fs] -> (let R (ygg.tpf-forms F)
                (if (hd R)
                    [true (tl R) | Fs]
                    (let S (ygg.tpf-files Fs)
                      [(hd S) (tl R) | (tl S)]))))

(define ygg.tpf-forms
  [] -> [false]
  [[defun | D] | Fs] -> (let R (ygg.tpf-forms Fs)
                          [(hd R) [defun | D] | (tl R)])
  [F | Fs] -> [true [set ygg.*trace-phase* 112] F | Fs])

(define ygg.tpf-append-last
  [] -> []
  [Last] -> [(append Last [[set ygg.*trace-phase* 112]])]
  [F | Fs] -> [F | (ygg.tpf-append-last Fs)])

\\ A user file's toplevel (non-defun) forms get the (value V) rewrite but
\\ no entry advice: they are not a join point, they are the boot itself.
\\
\\ shen.initialise OPENS the trace and starts the boot phase: ygg.trace-open
\\ runs first, so the stream exists before any entry advice, including the
\\ initialiser's own, and sets the phase to boot.  Everything the initialiser
\\ does - the 20,000 shen.fillvector calls among it - is therefore tagged b.
\\ The flip to the program phase is NOT here; it is the first non-defun
\\ toplevel form of the user's files.  See ygg.trace-phase-flip for why the
\\ two are not the same point in an unshaken artifact.
(define ygg.trace-defun
  [defun shen.initialise Args Body]
   -> [defun shen.initialise Args
        [do [ygg.trace-open]
            [do [ygg.traced shen.initialise]
                (ygg.trace-values Body)]]]
  [defun F Args Body]
   -> [defun F Args [do [ygg.traced F] (ygg.trace-values Body)]]
  Form -> (ygg.trace-values Form))

\\ (value V) -> (ygg.traced-value V).  Two clauses, not one:
\\
\\   - a literal symbol V is passed through as itself (symbols
\\     self-evaluate in KL), which is the common case and the one the
\\     shake's own readsIn/reads extractors can see;
\\   - anything else - the eta-wrapper trim-top builds for the `value`
\\     primitive is literally (lambda X1 (value X1)), and a user program can
\\     write (value (intern "shen.*tc*")) - is woven too, with the
\\     sub-expression itself traced first.  ygg.traced-value is a DEFUN, so
\\     its argument is evaluated exactly once: (ygg.traced-value (intern
\\     "shen.*tc*")) interns once and records the symbol that came out.
\\
\\ The second clause is what gives the read half of the trace teeth.  A
\\ computed global name is precisely the read the shake cannot see - no
\\ symbol for rawsym to keep alive, no readsIn row, no reads row - so it is
\\ precisely the read that stage 4 may have pruned the initialiser for, and
\\ leaving it uninstrumented meant the only artifact that could have shown
\\ it never recorded it.  See trace.go's prunedReadViolations.
(define ygg.trace-values
  [value V] -> [ygg.traced-value V]  where (ygg.cn-literal-sym? V)
  [value V] -> [ygg.traced-value (ygg.trace-values V)]
  [X | Y] -> [(ygg.trace-values X) | (ygg.trace-values Y)]
  X -> X)

\\ ---------------------------- the advice --------------------------------
\\ Emitted as KL into kernel.kl, after the woven defuns.  The stream lives
\\ in a global that shen.initialise opens as its very first form, before
\\ its own entry advice fires, so no traced entry can ever run before the
\\ stream exists - including the entries inside the initialiser itself.
\\
\\ Line format, one record per line, chosen so the dedup on the Go side is
\\ a string split and so a human can read the file:
\\   f<TAB>NAME<TAB>PHASE   a defun entry
\\   v<TAB>NAME<TAB>PHASE   a global read
\\   e<TAB>end              the end-of-run record, written once
\\ PHASE is one byte: b while shen.initialise is running, p afterwards.
\\ Without it the trace conflates the boot with the program, and the
\\ overwhelming majority of records in any run belong to the boot.
\\
\\ The end record is what makes a truncated trace detectable: it is written
\\ from the last user toplevel form, immediately before the stream is
\\ closed, so a trace without it is a run that did not finish or a tail the
\\ port never flushed.  `close` is a KL primitive already in the manifest's
\\ primitive list, so the fallback introduces no new capability.
\\
\\ The KL variable names are interned rather than written literally: a bare
\\ uppercase S in a Shen body is a free variable and `define` rejects it,
\\ the same reason (value *shake-rules*) goes through ygg.dl-varify.
(define ygg.trace-helpers
  -> (let S   (intern "S")
          Stm (intern "Stm")
          Tag (intern "Tag")
          F   (intern "F")
          V   (intern "V")
       [[defun ygg.trace-open []
          [do [set ygg.*trace-phase* 98]
              [set ygg.*trace-stream* [open (value ygg.*trace-file*) out]]]]
        [defun ygg.trace-str [S Stm]
          [if [= S ""]
              Stm
              [do [write-byte [string->n [pos S 0]] Stm]
                  [ygg.trace-str [tlstr S] Stm]]]]
        \\ Tag is a byte (102 = "f", 118 = "v", 101 = "e"), and the
        \\ separators, the phase byte and the terminator are written as
        \\ bytes too, so kernel.kl carries no string literal with a
        \\ control character in it.
        [defun ygg.trace-line [Tag S]
          [let Stm [value ygg.*trace-stream*]
            [do [write-byte Tag Stm]
                [do [write-byte 9 Stm]
                    [do [ygg.trace-str S Stm]
                        [do [write-byte 9 Stm]
                            [do [write-byte [value ygg.*trace-phase*] Stm]
                                [write-byte 10 Stm]]]]]]]]
        \\ The end-of-run record carries no phase: it is the boundary, not
        \\ something that happened inside one.
        [defun ygg.trace-end []
          [let Stm [value ygg.*trace-stream*]
            [do [write-byte 101 Stm]
                [do [write-byte 9 Stm]
                    [do [ygg.trace-str "end" Stm]
                        [do [write-byte 10 Stm]
                            [close Stm]]]]]]]
        [defun ygg.traced [F]
          [do [ygg.trace-line 102 [str F]] F]]
        [defun ygg.traced-value [V]
          [do [ygg.trace-line 118 [str V]] [value V]]]]))

\\ --------------------------- manifest keys ------------------------------

(define ygg.trace-manifest-sexp
  Sink -> done  where (not (value ygg.*trace*))
  Sink -> (do (pr-kl-line ["traced" true] Sink)
              (pr-kl-line ["trace-file" (value ygg.*trace-file*)] Sink)))

(define ygg.trace-manifest-txt
  Sink -> done  where (not (value ygg.*trace*))
  Sink -> (do (pr (make-string "traced=true~%") Sink)
              (pr (make-string "trace-file=~A~%" (value ygg.*trace-file*)) Sink)))

\\ ------------------------- the containment check ------------------------
\\ The obligation this discharges empirically is obligation 1 of the design
\\ note: everything the artifact can call is in the footprint.  A run gives
\\ one witness set; `called(F), kernel(F), !reach(F)` must be empty.
\\
\\ These rules are the mirror of the `called` / `readGlobal` block at the
\\ foot of analysis/analysis.dl, and they are a SEPARATE program from
\\ (value *shake-rules*) on purpose: their EDB (initwrite, defunwrite) is
\\ derived from trim-top's output, which the shake only has AFTER the
\\ shake rules have run, and a stratum that derives nothing has no business
\\ costing every shake a round.  Read the two side by side.
\\
\\   uncoveredCall(F) :- called(F), kernel(F), !reach(F).
\\   uncoveredRead(V) :- readGlobal(V), !initwrite(V), !defunwrite(V),
\\                       !portGlobal(V).
\\
\\ `kernel(F)` is what keeps user defuns, the five helpers and the
\\ synthesised shen.initialise out of the check: none of them is a row of
\\ the kernel call graph, so none of them is something `reach` could have
\\ derived, and a run entering them says nothing about the footprint.

(set *trace-rules*
  (ygg.dl-varify
   [[[[uncoveredCall f] [called f] [kernel f] [not [reach f]]]]
    [[[uncoveredRead g] [readGlobal g] [not [initwrite g]]
                        [not [defunwrite g]] [not [portGlobal g]]]]]))

\\ Globals written by the kept toplevel forms - the kernel's initialiser
\\ and then the user files' toplevel forms, in boot order.  This is the
\\ column projection of stage 4's `writes`, which is why analysis.dl derives
\\ initwrite from it rather than reading a fact file; here it is computed
\\ directly, because the trace rules want it as a one-column EDB relation
\\ and ygg.io-writes (the init-order check) already produces exactly that
\\ set, at the same depth and with the same defun/lambda/freeze exclusions.
(define ygg.trace-initwrites
  Tops UserKL -> (ygg.remove-dups
                  (mapcan (fn ygg.io-writes)
                          (append Tops (ygg.user-tops UserKL)))))

\\ Globals written from INSIDE a kept defun.  Not part of stage 4's
\\ liveGlobal story - a set that only ever runs when a function is called
\\ does not keep an initialiser alive - but it is what stops uncoveredRead
\\ from flagging every counter the kernel maintains at run time
\\ (shen.*call*, shen.*gensym* and friends are set and read inside defuns,
\\ never at toplevel).
(define ygg.trace-defunwrites
  Foot Kernel UserKL -> (ygg.remove-dups
                         (append (mapcan (fn ygg.trace-body-writes)
                                         (footcode Foot Kernel))
                                 (mapcan (/. Fs (mapcan (fn ygg.trace-body-writes) Fs))
                                         UserKL))))

(define ygg.trace-body-writes
  [defun _ _ Body] -> (ygg.trace-sets Body)
  _ -> [])

(define ygg.trace-sets
  [set V Val] -> [V | (ygg.trace-sets Val)]  where (ygg.cn-literal-sym? V)
  [X | Y] -> (append (ygg.trace-sets X) (ygg.trace-sets Y))
  _ -> [])

\\ (yggdrasil.trace-check Files FactsDir): re-run the shake's analysis over
\\ Files, load the run's called/readglobal facts out of FactsDir, close the
\\ trace rules over the lot and report.  Output contract, the Go driver
\\ parses these and nothing else:
\\   yggdrasil-trace-check: OK called=N reach=M counts=verified
\\   yggdrasil-trace-check: OK called=N reach=M counts=unverified
\\   yggdrasil-trace-check: FAIL uncovered=F,G
\\   yggdrasil-trace-check: FAIL truncated=shen.initialise-not-entered
\\   yggdrasil-trace-check: FAIL truncated=called-count read=25 declared=34
\\   yggdrasil-trace-check: FAIL truncated=no-end-record
\\
\\ The truncated= forms are the host half's own guard against a called.facts
\\ that is not the run's, and they are what stops a cut-off relation from
\\ reading as a clean run.  There are two, because one alone was not a
\\ prefix detector:
\\
\\   shen.initialise-not-entered.  Every traced run enters shen.initialise -
\\   the weaver puts the entry advice inside it - so its absence means the
\\   relation is not a run's.  called.facts is written SORTED, though, so
\\   this catches only a cut short enough to drop that one name: for fib it
\\   is line 21 of 34, and `head -25` sailed past it.
\\
\\   called-count.  The writer of the facts declares, in FactsDir/trace.meta,
\\   how many rows it wrote and whether the trace it read carried its
\\   end-of-run record; a called.facts with a different number of rows is not
\\   the file that was written.  THIS is the strict-prefix detector, and it
\\   catches any truncation, not a chosen name's.  It detects LOSS, not
\\   forgery: a hand that rewrites trace.meta too is writing a fiction, and
\\   no check on the same directory can say otherwise.
\\
\\ A FactsDir with no trace.meta - a bare `yggdrasil facts` dump, a relation
\\ assembled by hand, a tree half-updated - is not refused, because the
\\ counts were never declared there.  It reports counts=unverified, so the
\\ OK line says which of the two guards actually ran rather than implying
\\ both.  Both are properties of the FACT FILE, checkable here with no
\\ artifact in sight, which is the half the Go driver's end-of-run record
\\ cannot reach.
(define yggdrasil.trace-check
  Files FactsDir
   -> (let P        (ygg.pipeline Files)
           Kernel   (ygg.pl-kernel P)
           EvalFree (ygg.pl-evalfree P)
           KL       (ygg.pl-kl P)
           Tops     (ygg.pl-tops P)
           Foot     (ygg.pl-foot P)
           Arities  (arity-literal Tops)
           TopsOut  (map (/. T (trim-top T Foot EvalFree Arities)) Tops)
           Called   (ygg.trace-read (@s FactsDir "/called.facts"))
           Reads    (ygg.trace-read (@s FactsDir "/readglobal.facts"))
           Meta     (ygg.trace-read (@s FactsDir "/trace.meta"))
           Add      (ygg.mapc (fn ygg.dl-add)
                     (append (map (/. F [called F]) Called)
                     (append (map (/. V [readGlobal V]) Reads)
                     (append (map (/. V [initwrite V])
                                  (ygg.trace-initwrites TopsOut KL))
                     (append (map (/. V [defunwrite V])
                                  (ygg.trace-defunwrites Foot Kernel KL))
                             (map (/. V [portGlobal V])
                                  (value *global-primitives*)))))))
           \\ These strata run against the database the shake run left
           \\ standing, so their closed set is the relations Add just
           \\ loaded plus the shake's own heads, named here because this
           \\ path builds its EDB by hand rather than through ygg.dl-run.
           Run      (ygg.dl-strata [called readGlobal initwrite defunwrite
                                    portGlobal reach kernel]
                                   (value *trace-rules*))
           Bad      (append (ygg.dl-col1 uncoveredCall) (ygg.dl-col1 uncoveredRead))
           Restore  (ygg.pl-restore P)
           Report   (ygg.trace-report (ygg.trace-provenance Meta Called) Bad
                                      (ygg.len Called)
                                      (ygg.len (ygg.dl-col1 reach)))
           done))

\\ Is this relation the one some run wrote?  Two independent answers, the
\\ first of which needs nothing but called.facts itself; see the note above
\\ the definition of yggdrasil.trace-check.  The result is [ok Note] or
\\ [fail Why], both carrying the string the report line prints.
(define ygg.trace-provenance
  _ Called -> [fail "shen.initialise-not-entered"]
               where (not (ygg.trace-entered? Called))
  Meta Called -> (ygg.trace-counts (ygg.trace-meta-get complete Meta)
                                   (ygg.trace-meta-get called Meta)
                                   (ygg.len Called)))

(define ygg.trace-entered?
  Called -> (element? shen.initialise Called))

\\ The writer's declared row count against the rows actually present.  A
\\ missing declaration is reported, not failed: it means nobody promised a
\\ count for this directory, which is a different thing from a broken one.
(define ygg.trace-counts
  [] _ _ -> [ok "unverified"]
  _ [] _ -> [ok "unverified"]
  Complete _ _ -> [fail "no-end-record"]  where (not (= Complete true))
  _ Declared N -> [ok "verified"]  where (= (str Declared) (str N))
  _ Declared N -> [fail (@s "called-count read=" (@s (str N)
                            (@s " declared=" (str Declared))))])

\\ One TAB-separated key/value pair per line, read by ygg.trace-read into a
\\ flat token list; [] for a key that is not there.  Values are symbols, so
\\ [] is unambiguously "absent".
(define ygg.trace-meta-get
  _ [] -> []
  K [Key V | _] -> V  where (= K Key)
  K [_ | Rest] -> (ygg.trace-meta-get K Rest))

(define ygg.trace-report
  [fail Why] _ _ _ -> (pr (make-string "yggdrasil-trace-check: FAIL truncated=~A~%" Why)
                          (stoutput))
  [ok Note] [] NCalled NReach
   -> (pr (make-string "yggdrasil-trace-check: OK called=~A reach=~A counts=~A~%"
                       NCalled NReach Note)
          (stoutput))
  _ Bad _ _ -> (pr (make-string "yggdrasil-trace-check: FAIL uncovered=~A~%"
                                (ygg.cn-commas (ygg.remove-dups Bad)))
                   (stoutput)))

\\ One interned symbol per non-empty line of a one-column .facts file; a
\\ missing file is an empty relation.  Byte-level, like parse-graph, and
\\ for the same reason: the Shen reader would curry and re-intern.
(define ygg.trace-read
  File -> (trap-error (ygg.trace-lines (read-file-as-bytelist File) "" [])
                      (/. E [])))

(define ygg.trace-lines
  [] Token Acc -> (reverse (close-token Token Acc))
  [10 | Bs] Token Acc -> (ygg.trace-lines Bs "" (close-token Token Acc))
  [13 | Bs] Token Acc -> (ygg.trace-lines Bs Token Acc)
  [9 | Bs] Token Acc -> (ygg.trace-lines Bs "" (close-token Token Acc))
  [B | Bs] Token Acc -> (ygg.trace-lines Bs (cn Token (n->string B)) Acc))
\\ ===================== stage 5: the unshaken build ======================
\\ Stage 5 of docs/analysis-rules.md needs two artifacts built by the SAME
\\ stage-2 builder: the shaken program A* and the full program A = K + user,
\\ so that an index of each can be compared node for node.  Only A* has an
\\ existing entry point; this section is A.
\\
\\ (yggdrasil.shake-full Files Dir) is yggdrasil.shake with every decision
\\ that removes something turned off:
\\   - no eval-strip: the toplevel init forms go out exactly as the kernel
\\     wrote them - macro table, 161 declares, build-lambda-table and all -
\\     so the artifact is the eval-capable one;
\\   - no footprint: FootCode is every kernel defun, in kernel load order,
\\     which is what footcode returns when the footprint is defun-names;
\\   - no trim-top and no rewrite-f-error: the mode flag those two take is
\\     false, so both are identities.
\\ Everything else - the writer, the user pipeline, the init-order check,
\\ the manifest - is shared with the shake, which is the point: A and A*
\\ differ only by the shake, not by the code path that emitted them.
\\
\\ The manifest records shaken=false.  It is written ONLY in this mode: a
\\ shaken manifest stays byte-identical to the one the pre-stage-5 binary
\\ wrote, and a builder that has never heard of the key reads its absence
\\ as the shaken default, which is what every manifest before this one was.
(set ygg.*shake-full* false)

(define yggdrasil.shake-full
  Files Dir -> (let Set   (set ygg.*shake-full* true)
                    Done  (trap-error (ygg.shake-full-h Files Dir)
                                      (/. E (ygg.shake-full-fail E)))
                    Unset (set ygg.*shake-full* false)
                    Done))

\\ (yggdrasil.shake-full-traced Files Dir): the full program A, WOVEN.
\\
\\ It exists for one question, `yggdrasil trace-check --full`, and it is a
\\ separate entry point rather than a --no-shake that also accepts --trace
\\ because those two flags still mean what trace.go's shakeExpr says they
\\ mean and are still refused together.  The question this one answers:
\\ trace the program with every kernel defun present, so a call the slice
\\ does NOT contain is recorded instead of crashing, and check the
\\ program-phase called set against the SLICE's reach.  That check can fail;
\\ the containment check run against a slice cannot, because there the name
\\ is not in the artifact at all (see the head of trace.go).
\\
\\ The boot phase of a full artifact legitimately enters code outside the
\\ slice -- the eval-capable initialiser is exactly what the shake threw
\\ away -- so the phase split is not a convenience here, it is what makes
\\ the answer readable at all.
(define yggdrasil.shake-full-traced
  Files Dir -> (let On  (set ygg.*trace* true)
                    R   (trap-error (yggdrasil.shake-full Files Dir)
                                    (/. E (ygg.trace-off-then E)))
                    Off (set ygg.*trace* false)
                    R))

\\ The flag is process-global, so a failed full shake must not leave it set
\\ for a later shake in the same host image (the facts and footprints entry
\\ points do run several pipelines per process).
(define ygg.shake-full-fail
  E -> (let Unset (set ygg.*shake-full* false)
            (simple-error (error-to-string E))))

(define ygg.shake-full-h
  Files Dir -> (let MaxPrint  (value *maximum-print-sequence-size*)
                    Unlimit   (set *maximum-print-sequence-size* 1000000000)
                    Kernel    (kernel-code)
                    KLFiles   (map (fn bootstrap) Files)
                    KL        (map (fn read-file) KLFiles)
                    Tops      (toplevel-forms Kernel)
                    Foot      (defun-names Kernel)
                    FootCode  (footcode Foot Kernel)
                    CNames    (ygg.cn-direct KL)
                    Warn      (ygg.cn-warn CNames)
                    InitOrder (ygg.init-order-check Tops KL)
                    InitDefun (synthesize-initialise Tops)
                    \\ Woven exactly where the shake weaves, and with the
                    \\ same two functions: both are the identity while
                    \\ ygg.*trace* is false, so an ordinary --no-shake build
                    \\ is byte-for-byte the one this wrote before
                    \\ yggdrasil.shake-full-traced existed.
                    OutCode   (ygg.trace-kernel (append FootCode [InitDefun]))
                    UserKL    (ygg.trace-user KL)
                    Prims     (find-primitives (append OutCode UserKL))
                    WriteK    (write-kl-file (@s Dir "/kernel.kl") OutCode)
                    UserOut   (write-user-files KLFiles UserKL Dir)
                    \\ A full build prunes nothing, so pruned-init is 0, and
                    \\ InitOrder carries the check's own answer.  Neither
                    \\ argument is optional: write-manifest took a fifth
                    \\ argument when this was written, a sixth once stage 4
                    \\ landed and a seventh once init-order= stopped being a
                    \\ constant, and a short call here does not fail - Shen
                    \\ curries it into a closure this `let` then discards, so
                    \\ the full build silently wrote no manifest at all.
                    WriteM    (write-manifest Dir UserOut UserKL Prims CNames 0
                                              InitOrder)
                    Restore   (set *maximum-print-sequence-size* MaxPrint)
                    Report    (pr (make-string "yggdrasil-shake: shaken=false defuns=~A~%"
                                               (ygg.len FootCode))
                                  (stoutput))
                    done))

\\ computedName without the Datalog engine.  A full build runs no rule set
\\ (there is no footprint to derive), but the manifest key has to keep its
\\ meaning, so derive it straight from the same facts ygg.cn-facts extracts
\\ for the rules: computedName(F) :- userintern(F) ; userglobal(F).
(define ygg.cn-direct
  KL -> (ygg.remove-dups (map (fn ygg.cn-row-name) (ygg.cn-facts KL))))

(define ygg.cn-row-name
  [_ F] -> F)

\\ Stage 5.  Written only by yggdrasil.shake-full, so a shaken manifest is
\\ byte-for-byte the one the pre-stage-5 binary wrote; absence of the key
\\ means shaken, the only thing any manifest has ever meant.
(define ygg.shaken-line-sexp
  Sink -> (if (value ygg.*shake-full*)
              (pr-kl-line ["shaken" false] Sink)
              done))

(define ygg.shaken-line-txt
  Sink -> (if (value ygg.*shake-full*)
              (pr (make-string "shaken=false~%") Sink)
              done))
