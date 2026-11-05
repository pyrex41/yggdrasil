\\ Yggdrasil stage-2 Shen/JIT builder: the KLambda half.
\\
\\ Run under a Shen/JIT executable from builders/jit/build.sh, after it has
\\ loaded Shen/JIT's own stage 0 tools (src/lowerer/host.shen, the renamed
\\ lowerer and stage0.shen without its last form):
\\
\\   (yggdrasil.jit.build Slice Users Eval PortFiles Universe Out)
\\
\\ Lowers the shaken kernel, the user files and the part of the port they reach
\\ into Out/<file>.sbc, the input of `runtime --stage1`.  The artifact holds
\\ this slice instead of Shen/JIT's image of the whole kernel.
\\
\\   Port overrides   A port defun replaces the slice's defun of the same name,
\\                    as stage 0 does for the whole kernel.
\\   Port closure     Starting from those overrides and the program, a port
\\                    defun is kept when any kept form names it.  Naming is
\\                    conservative: a symbol in data keeps a function too.
\\   Port support     A kernel function that kept port code names and the slice
\\                    does not have is taken from Universe and listed in
\\                    jit-port-support.txt, since it runs outside the manifest.
\\   Stage 1          It reads 24 fixed file names and calls four initialisers,
\\                    which exist here as the definitions under "Stubs".  The
\\                    slice and the port go in a sealed file, user code in an
\\                    open one, as Shen/JIT seals the kernel and not programs.
\\   Entry            shen-jit.*entry* is set while stage 1 runs, so the image
\\                    is a program.  When the artifact starts, its entry does what
\\                    every builder's boot does: the kernel's toplevel forms, then
\\                    shen.initialise, then the user's forms in manifest order.
\\                    Stage 1 calls shen.initialise too, by name, so the slice's is
\\                    guarded to do nothing until the entry has started.

\\ read-from-string-unprocessed, from bytes.  Going through a string costs memory
\\ quadratic in its length, as shen.str->bytes keeps each tlstr of it on the stack:
\\ more than 500 MB for an eval-capable slice's 300 KB.
(define yggdrasil.jit.read
  File -> (compile (/. X (shen.<s-exprs> X)) (read-file-as-bytelist File)))

(define yggdrasil.jit.defun?
  [defun F Params | _] -> true
  _ -> false)

(define yggdrasil.jit.defuns
  Forms -> (stage0.filter (/. X (yggdrasil.jit.defun? X)) Forms))

\\ Toplevel strings in KLambda are copyright notices.
(define yggdrasil.jit.tops
  Forms -> (stage0.filter (/. X (and (cons? X) (not (yggdrasil.jit.defun? X)))) Forms))

(define yggdrasil.jit.name
  [defun F | _] -> F)

(define yggdrasil.jit.names
  Defuns -> (map (/. D (yggdrasil.jit.name D)) Defuns))

\\ A dictionary from a defun's name to the defun, built once: the closure below
\\ asks it about every symbol of every kept form.
(define yggdrasil.jit.table
  Defuns -> (let T (shen.dict 4096)
                 W (map (/. D (shen.dict-> T (yggdrasil.jit.name D) D)) Defuns)
              T))

(define yggdrasil.jit.lookup
  T K -> (trap-error (shen.<-dict T K) (/. E [])))

\\ The symbols a form can call: everything but quoted data, which KLambda spells
\\ as cons trees.  A symbol in data can only be called through a computed name,
\\ which the shake reports and refuses to count on.
(define yggdrasil.jit.symbols
  [cons X Y] Acc -> (yggdrasil.jit.symbols Y Acc) where (shen-jit.symbol? X)
  [X | Xs] Acc -> (yggdrasil.jit.symbols Xs (yggdrasil.jit.symbols X Acc))
  X Acc -> [X | Acc] where (shen-jit.symbol? X)
  _ Acc -> Acc)

\\ eval-kl is lowered as a call to shen-jit.eval-kl (src/runtime/prims.c), so the
\\ name that keeps the port's eval and the lowerer is the primitive's.  An
\\ eval-free slice does not follow it: the port's fn still names eval-kl, to make
\\ a lazy lambda form, but only a definition evaluated at run time stores one.
(define yggdrasil.jit.alias
  true eval-kl -> shen-jit.eval-kl
  _ S -> S)

\\ Follows the Todo forms to [Port | Support], the defuns kept from the port and
\\ those taken from the universe.  Kept is the set of defined names.
(define yggdrasil.jit.close
  [] _ _ _ _ Port Support -> [Port | Support]
  [Form | Todo] Eval Kept PortT UniT Port Support
  -> (yggdrasil.jit.close-names
       (map (/. S (yggdrasil.jit.alias Eval S)) (yggdrasil.jit.symbols Form []))
       Todo Eval Kept PortT UniT Port Support))

(define yggdrasil.jit.close-names
  [] Todo Eval Kept PortT UniT Port Support -> (yggdrasil.jit.close Todo Eval Kept PortT UniT Port Support)
  [S | Ss] Todo Eval Kept PortT UniT Port Support
  -> (yggdrasil.jit.close-names Ss Todo Eval Kept PortT UniT Port Support)
     where (cons? (yggdrasil.jit.lookup Kept S))
  [S | Ss] Todo Eval Kept PortT UniT Port Support
  -> (let P (yggdrasil.jit.lookup PortT S)
          U (yggdrasil.jit.lookup UniT S)
       (cond
         ((cons? P) (do (shen.dict-> Kept S [S])
                        (yggdrasil.jit.close-names Ss [P | Todo] Eval Kept PortT UniT [P | Port] Support)))
         ((cons? U) (do (shen.dict-> Kept S [S])
                        (yggdrasil.jit.close-names Ss [U | Todo] Eval Kept PortT UniT Port [U | Support])))
         (true (yggdrasil.jit.close-names Ss Todo Eval Kept PortT UniT Port Support)))))

\\ What stage 1 calls by name.  A slice has no launcher and no extensions, and
\\ the port's shen-jit.initialise registers primitives and thread through
\\ functions a slice does not carry.
(define yggdrasil.jit.stubs
  Eval -> [[defun shen.x.features.initialise [features] true]
      [defun shen.x.programmable-pattern-matching.initialise [] true]
      [defun shen.x.namespaces.initialise [] true]
      [defun shen-jit.initialise []
        [do [set shen-jit.defined []] [set shen-jit.*entry* yggdrasil.jit.main]]]
      | (yggdrasil.jit.no-eval Eval)])

\\ The lowerer calls shen-jit.eval-kl sealed, so the name must exist.  In an
\\ eval-free slice nothing reaches it, and if the shake were wrong this says so.
(define yggdrasil.jit.no-eval
  true -> []
  false -> [[defun shen-jit.eval-kl [x]
              [simple-error "this slice was shaken eval-free and carries no compiler"]]])

(define yggdrasil.jit.index
  [] _ -> []
  [X | Xs] N -> [[N | X] | (yggdrasil.jit.index Xs (+ N 1))])

(define yggdrasil.jit.top-name
  N -> (concat yggdrasil.jit.top- N))

\\ One function per toplevel form, as a long program would otherwise be one
\\ enormous frame.
(define yggdrasil.jit.top-defuns
  [] -> []
  [[N | Form] | Rest] -> [[defun (yggdrasil.jit.top-name N) [] Form] | (yggdrasil.jit.top-defuns Rest)])

(define yggdrasil.jit.main-body
  [] -> true
  [[N | _] | Rest] -> [do [(yggdrasil.jit.top-name N)] (yggdrasil.jit.main-body Rest)])

(define yggdrasil.jit.boot-global
  -> yggdrasil.jit.*booting*)

(define yggdrasil.jit.guard-initialise
  [defun shen.initialise [] Body]
  -> [defun shen.initialise []
       [if [trap-error [value (yggdrasil.jit.boot-global)] [lambda e false]] Body true]]
  D -> D)

\\ An eval-capable program can define code that calls the primitives, whose
\\ arities the compiler looks up once the property vector exists.
(define yggdrasil.jit.boot-body
  [] UIndex Eval -> [do [shen.initialise]
                        (yggdrasil.jit.after-initialise Eval (yggdrasil.jit.main-body UIndex))]
  [[N | _] | Rest] UIndex Eval
  -> [do [(yggdrasil.jit.top-name N)] (yggdrasil.jit.boot-body Rest UIndex Eval)])

(define yggdrasil.jit.after-initialise
  true Body -> [do [shen-jit.register-primitives] Body]
  false Body -> Body)

\\ What stage 1's port initialiser defines for a whole image, as a port defun: a
\\ program on Shen/JIT can call thread without declaring it.
(define yggdrasil.jit.thread
  -> [defun thread [x] [shen-jit.thread x]])

(define yggdrasil.jit.lower
  Forms Sealed -> (shen-jit.stage0.lower-file Forms Sealed))

(define yggdrasil.jit.write
  Out File Lowered -> (shen-jit.stage0.write-file (cn Out (cn "/" (cn File ".sbc"))) Lowered))

\\ The 24 names of stage0_files in src/runtime/main.c, sealed files first.
(define yggdrasil.jit.empty-files
  -> ["core" "sys" "dict" "sequent" "yacc" "reader" "prolog" "track" "load" "writer" "macros"
      "declarations" "types" "t-star" "init" "shen-jit" "lower"
      "extension-launcher" "extension-programmable-pattern-matching" "extension-namespaces"
      "extension-expand-dynamic" "extension-type-annotations"])

(define yggdrasil.jit.write-lines
  Path Names -> (let S (open Path out)
                     W (map (/. N (pr (make-string "~A~%" N) S)) Names)
                  (close S)))

(define yggdrasil.jit.build
  Slice Users Eval PortFiles Universe Out
  -> (let KForms (yggdrasil.jit.read (cn Slice "/kernel.kl"))
          UForms (mapcan (/. U (yggdrasil.jit.read (cn Slice (cn "/" U)))) Users)
          PortDs (yggdrasil.jit.defuns (mapcan (/. F (yggdrasil.jit.read F)) PortFiles))
          Ports  [(yggdrasil.jit.thread)
                  | (stage0.filter (/. D (not (= (yggdrasil.jit.name D) shen-jit.initialise))) PortDs)]
          UniDs  (yggdrasil.jit.defuns (mapcan (/. F (yggdrasil.jit.read F)) Universe))
          PortT  (yggdrasil.jit.table Ports)
          UniT   (yggdrasil.jit.table UniDs)
          KDs    (yggdrasil.jit.defuns KForms)
          KTops  (yggdrasil.jit.tops KForms)
          UDs    (yggdrasil.jit.defuns UForms)
          UTops  (yggdrasil.jit.tops UForms)
          \\ The slice's defuns that the port overrides are replaced, not kept.
          Stubs  (yggdrasil.jit.stubs Eval)
          Kept   (yggdrasil.jit.table [])
          Slicing (map (/. N (shen.dict-> Kept N [N]))
                       (append (yggdrasil.jit.names KDs) (yggdrasil.jit.names UDs)))
          Stubbed (map (/. N (shen.dict-> Kept N [N])) (yggdrasil.jit.names Stubs))
          Overrides (stage0.filter (/. D (cons? (yggdrasil.jit.lookup PortT (yggdrasil.jit.name D)))) KDs)
          Replaced (map (/. D (yggdrasil.jit.lookup PortT (yggdrasil.jit.name D))) Overrides)
          KRest  (stage0.filter (/. D (not (cons? (yggdrasil.jit.lookup PortT (yggdrasil.jit.name D))))) KDs)
          KIndex (yggdrasil.jit.index KTops 0)
          UIndex (yggdrasil.jit.index UTops (length KTops))
          Tops   (append KIndex UIndex)
          Entry  [defun yggdrasil.jit.main []
                   [do [set (yggdrasil.jit.boot-global) true]
                       (yggdrasil.jit.boot-body KIndex UIndex Eval)]]
          TopDs  (yggdrasil.jit.top-defuns Tops)
          Closed (yggdrasil.jit.close (append KRest (append Replaced (append UDs (append Stubs (append [Entry | Tops] []))))) Eval Kept PortT UniT [] [])
          Sealed (append (map (/. D (yggdrasil.jit.guard-initialise D)) KRest) (append Replaced (append (hd Closed) (append (tl Closed) (append Stubs [Entry | TopDs])))))
          Names  (yggdrasil.jit.names Sealed)
          \\ User code calls the sealed names directly and stays open itself,
          \\ as a program does on Shen/JIT.
          Tables (shen-jit.host.set-tables Sealed)
          W1 (yggdrasil.jit.write Out "toplevel" (yggdrasil.jit.lower Sealed Names))
          W2 (yggdrasil.jit.write Out "extension-features" (yggdrasil.jit.lower UDs Names))
          W3 (map (/. F (yggdrasil.jit.write Out F [])) (yggdrasil.jit.empty-files))
          W4 (yggdrasil.jit.write-lines (cn Out "/jit-port-support.txt") (yggdrasil.jit.names (tl Closed)))
          W5 (yggdrasil.jit.write-lines (cn Out "/jit-port-kept.txt") (yggdrasil.jit.names (append Replaced (hd Closed))))
       (output "jit: ~A slice, ~A port, ~A support, ~A user defuns; ~A toplevel forms~%"
               (length KRest) (+ (length Replaced) (length (hd Closed))) (length (tl Closed))
               (length UDs) (length Tops))))
