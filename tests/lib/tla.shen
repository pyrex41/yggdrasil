\* tla.shen -- TLA+-style model checking and trace validation, in Shen.

   A system is two values:

     Init   a list of initial states
     Next   a function from a state to its successors. Each successor is
            (@p Action State), where Action is any value naming the step
            that produced it: a symbol, or a list such as [vote b a].
            Nondeterminism is the list; each element is one disjunct of
            TLA+'s Next.

   States are any Shen data built from lists, tuples, symbols, strings,
   numbers and booleans. Two states are the same state when they are `=`.

   (tla.check Init Next Options) explores every reachable state breadth
   first, as TLC does, and checks the properties in Options:

     [invariant Name P]         [] P                P : state --> boolean
     [step-invariant Name R]    [][R]_vars          R : state --> state --> boolean
     [eventually Name P]        <> P, under WF(Next)
     [leads-to Name P Q]        P ~> Q, under WF(Next)
     [possible Name P]          AG EF P (CTL; not expressible in TLA+)
     [wf Name Sel]              weak fairness for actions A with (Sel A)
     [sf Name Sel]              strong fairness for actions A with (Sel A)
     [deadlock false]           allow states with no successors
     [max-states N]             stop after N states (0 = no bound)

   Functions may be given as lambdas, (fn f), or plain symbols.

   Liveness always assumes WF(Next): a behaviour never stutters forever
   while some action is enabled; it ends only by stuttering in a state
   with no successors. wf/sf add per-action fairness on top.

   The result is one of

     [ok States Transitions Depth]
     [incomplete States Transitions Depth]        max-states reached
     [violation Kind Name Trace Loop States Transitions Depth]

   where Kind is invariant, step-invariant, deadlock, liveness or
   possibility, and Trace is a list of (@p Action State) from an initial
   state (whose action is init). Safety counterexamples are shortest.
   For liveness, Loop = -1 means the last state stutters forever with
   nothing enabled; Loop = K means the behaviour returns from the last
   state to Trace element K and repeats.

   (tla.validate Init Next Options Trace) checks that a trace recorded
   from an implementation -- a list of states or (@p Action State) -- is a
   behaviour of the spec: it starts in an initial state (unless Options
   holds [from-any true]), every step is a stutter or an allowed
   successor (with the same action, when one is recorded), and every
   state and step satisfies the invariants. The result is ok or
   [trace-failure Index Reason Allowed].

   (tla.report Result) prints a result, TLC style.

   Performance: the visited-state map below is portable Shen. A host can
   replace it with a native hash map by redefining tla.map-new,
   tla.map-get and tla.map-put after loading this file (see native/). *\

\\ ---------------------------------------------------------------------
\\ Growable arrays: absvector [count storage capacity], 0-based.

(define tla.arr-new
  -> (tla.arr-sized 64))

(define tla.arr-sized
  Cap -> (let A (absvector 3)
              Skip1 (address-> A 0 0)
              Skip2 (address-> A 1 (absvector Cap))
              Skip3 (address-> A 2 Cap)
            A))

(define tla.arr-size A -> (<-address A 0))
(define tla.arr-get A I -> (<-address (<-address A 1) I))
(define tla.arr-set A I X -> (do (address-> (<-address A 1) I X) X))

(define tla.arr-push
  A X -> (let N (<-address A 0)
              Cap (<-address A 2)
              Skip4 (if (= N Cap) (tla.arr-grow A N Cap) skip)
              Skip5 (address-> (<-address A 1) N X)
              Skip6 (address-> A 0 (+ N 1))
            N))

(define tla.arr-grow
  A N Cap -> (let New (absvector (* 2 Cap))
                  Skip7 (tla.copy (<-address A 1) New 0 N)
                  Skip8 (address-> A 1 New)
                (address-> A 2 (* 2 Cap))))

(define tla.copy
  From To I N -> To where (= I N)
  From To I N -> (do (address-> To I (<-address From I)) (tla.copy From To (+ I 1) N)))

(define tla.fill
  V X I N -> V where (= I N)
  V X I N -> (do (address-> V I X) (tla.fill V X (+ I 1) N)))

(define tla.filled N X -> (tla.fill (absvector (tla.max N 1)) X 0 N))

\\ ---------------------------------------------------------------------
\\ Portable hash map from states to node ids: absvector
\\ [count buckets capacity], each bucket a list of (@p Key Value).
\\ Hosts override tla.map-new / tla.map-get / tla.map-put natively.

(define tla.map-new -> (tla.map-sized 1024))

(define tla.map-sized
  Cap -> (let M (absvector 3)
              Skip9 (address-> M 0 0)
              Skip10 (address-> M 1 (tla.filled Cap []))
              Skip11 (address-> M 2 Cap)
            M))

\\ The value stored for K, or -1.
(define tla.map-get
  M K -> (tla.bucket-get K (<-address (<-address M 1) (tla.hash K (<-address M 2)))))

(define tla.bucket-get
  _ [] -> -1
  K [(@p K2 V) | Rest] -> (if (= K K2) V (tla.bucket-get K Rest)))

(define tla.map-put
  M K V -> (let Cap (<-address M 2)
                Buckets (<-address M 1)
                H (tla.hash K Cap)
                Skip12 (address-> Buckets H [(@p K V) | (<-address Buckets H)])
                N (+ 1 (<-address M 0))
                Skip13 (address-> M 0 N)
              (if (> N (* 2 Cap)) (tla.map-rehash M) M)))

(define tla.map-rehash
  M -> (let Old (<-address M 1)
            OldCap (<-address M 2)
            Cap (* 4 OldCap)
            New (tla.filled Cap [])
            Skip14 (tla.rehash-buckets Old New Cap 0 OldCap)
            Skip15 (address-> M 1 New)
            Skip16 (address-> M 2 Cap)
          M))

(define tla.rehash-buckets
  Old New Cap I N -> New where (= I N)
  Old New Cap I N -> (do (tla.rehash-bucket (<-address Old I) New Cap)
                         (tla.rehash-buckets Old New Cap (+ I 1) N)))

(define tla.rehash-bucket
  [] _ _ -> done
  [(@p K V) | Rest] New Cap -> (let H (tla.hash K Cap)
                                    Skip17 (address-> New H [(@p K V) | (<-address New H)])
                                  (tla.rehash-bucket Rest New Cap)))

\\ A structural hash in [0, M) using only +, * and comparisons, so it is
\\ cheap on every port. Atoms go through the kernel's hash; structure is
\\ mixed in polynomially and reduced after every step. Needs M >= 1024.
(define tla.hash X M -> (tla.h X 7 M))

(define tla.h
  [] H M -> (tla.mix H 5 M)
  [X | Xs] H M -> (tla.h Xs (tla.h X (tla.mix H 3 M) M) M)
  X H M -> (tla.h (snd X) (tla.h (fst X) (tla.mix H 11 M) M) M) where (tuple? X)
  X H M -> (tla.mix H (hash X 1000) M))

\\ (H * 31 + A) mod M, for H < M and A <= 1000 < M: the sum is below
\\ 32M, so five conditional subtractions reduce it.
(define tla.mix H A M -> (tla.reduce (+ (* H 31) A) M [16 8 4 2 1]))

(define tla.reduce
  X _ [] -> X
  X M [K | Ks] -> (tla.reduce (if (>= X (* K M)) (- X (* K M)) X) M Ks))

\\ ---------------------------------------------------------------------
\\ Options

(define tla.fun
  F -> (function F) where (symbol? F)
  F -> F)

\\ Every entry of Opts with the given key, minus the key.
(define tla.entries
  _ [] -> []
  Key [[Key | Args] | Opts] -> [Args | (tla.entries Key Opts)]
  Key [_ | Opts] -> (tla.entries Key Opts))

(define tla.opt
  Key Opts Default -> (let Es (tla.entries Key Opts)
                        (if (empty? Es) Default (hd (hd Es)))))

\\ Named functions [Name F] for a key, with F normalised.
(define tla.named
  Key Opts -> (map (/. E [(hd E) (tla.fun (hd (tl E)))]) (tla.entries Key Opts)))

\\ ---------------------------------------------------------------------
\\ The explored graph: absvector
\\ [states parents actions depths succs map transitions maxdepth]
\\ succs holds, per node, a list of (@p Action ToId).

(define tla.graph-new
  -> (let G (absvector 8)
          Skip18 (address-> G 0 (tla.arr-new))
          Skip19 (address-> G 1 (tla.arr-new))
          Skip20 (address-> G 2 (tla.arr-new))
          Skip21 (address-> G 3 (tla.arr-new))
          Skip22 (address-> G 4 (tla.arr-new))
          Skip23 (address-> G 5 (tla.map-new))
          Skip24 (address-> G 6 0)
          Skip25 (address-> G 7 0)
        G))

(define tla.size G -> (tla.arr-size (<-address G 0)))
(define tla.state G I -> (tla.arr-get (<-address G 0) I))
(define tla.parent G I -> (tla.arr-get (<-address G 1) I))
(define tla.action G I -> (tla.arr-get (<-address G 2) I))
(define tla.succs G I -> (tla.arr-get (<-address G 4) I))

(define tla.path-to
  G I -> (tla.path-to-h G I []))

(define tla.path-to-h
  G -1 Acc -> Acc
  G I Acc -> (tla.path-to-h G (tla.parent G I) [(@p (tla.action G I) (tla.state G I)) | Acc]))

(define tla.violation?
  [violation | _] -> true
  _ -> false)

\\ Label a successor: (@p Action State), or a bare state labelled next.
(define tla.label
  S -> S where (tuple? S)
  S -> (@p next S))

(define tla.broken
  _ [] -> none
  S [[Name P] | Ps] -> (if (P S) (tla.broken S Ps) Name))

(define tla.broken-step
  _ _ [] -> none
  S T [[Name R] | Rs] -> (if ((R S) T) (tla.broken-step S T Rs) Name))

\\ Add a state reached from Parent by Action; returns its id, or a
\\ violation if it is new and breaks an invariant.
(define tla.add
  G S Parent Action Invs -> (let Known (tla.map-get (<-address G 5) S)
                              (if (>= Known 0)
                                  Known
                                  (tla.add-new G S Parent Action Invs))))

(define tla.add-new
  G S Parent Action Invs
  -> (let Id (tla.arr-push (<-address G 0) S)
          Skip26 (tla.map-put (<-address G 5) S Id)
          Skip27 (tla.arr-push (<-address G 1) Parent)
          Skip28 (tla.arr-push (<-address G 2) Action)
          Depth (if (= Parent -1) 0 (+ 1 (tla.arr-get (<-address G 3) Parent)))
          Skip29 (tla.arr-push (<-address G 3) Depth)
          Skip30 (tla.arr-push (<-address G 4) [])
          Skip31 (if (> Depth (<-address G 7)) (address-> G 7 Depth) skip)
          Bad (tla.broken S Invs)
        (if (= Bad none) Id [violation invariant Bad (tla.path-to G Id) -1])))

(define tla.add-inits
  _ [] _ -> ok
  G [S | Ss] Invs -> (let R (tla.add G S -1 init Invs)
                       (if (tla.violation? R) R (tla.add-inits G Ss Invs))))

(define tla.bfs
  G Next Invs Steps Deadlock Max I
  -> (cases (>= I (tla.size G)) done
            (and (> Max 0) (>= (tla.size G) Max)) incomplete
            true (let R (tla.expand G Next Invs Steps Deadlock I)
                   (if (tla.violation? R)
                       R
                       (tla.bfs G Next Invs Steps Deadlock Max (+ I 1))))))

(define tla.expand
  G Next Invs Steps Deadlock I
  -> (let S (tla.state G I)
          Succs (map (fn tla.label) (Next S))
        (if (and Deadlock (empty? Succs))
            [violation deadlock none (tla.path-to G I) -1]
            (tla.expand-succs G Invs Steps I S Succs []))))

(define tla.expand-succs
  G _ _ I _ [] Edges -> (do (tla.arr-set (<-address G 4) I (reverse Edges)) ok)
  G Invs Steps I S [(@p A T) | Rest] Edges
  -> (let Skip36 (address-> G 6 (+ 1 (<-address G 6)))
          Bad (tla.broken-step S T Steps)
        (if (= Bad none)
            (let Id (tla.add G T I A Invs)
              (if (tla.violation? Id)
                  Id
                  (tla.expand-succs G Invs Steps I S Rest [(@p A Id) | Edges])))
            [violation step-invariant Bad (append (tla.path-to G I) [(@p A T)]) -1])))

(define tla.stats
  Tag G -> [Tag (tla.size G) (<-address G 6) (<-address G 7)])

(define tla.finish
  G [violation Kind Name Trace Loop]
  -> [violation Kind Name Trace Loop (tla.size G) (<-address G 6) (<-address G 7)])

(define tla.check
  Init Next Opts
  -> (let G (tla.graph-new)
          Invs (tla.named invariant Opts)
          Steps (tla.named step-invariant Opts)
          NextF (tla.fun Next)
          R (tla.add-inits G Init Invs)
        (if (tla.violation? R)
            (tla.finish G R)
            (let R2 (tla.bfs G NextF Invs Steps (tla.opt deadlock Opts true)
                             (tla.opt max-states Opts 0) 0)
              (cases (tla.violation? R2) (tla.finish G R2)
                     (= R2 incomplete) (tla.stats incomplete G)
                     true (let R3 (tla.temporal G Opts)
                            (if (tla.violation? R3)
                                (tla.finish G R3)
                                (tla.stats ok G))))))))

\\ ---------------------------------------------------------------------
\\ Temporal properties, on the complete graph

\\ A boolean absvector: P evaluated on every node.
(define tla.eval-pred
  G P -> (tla.eval-pred-h G P (absvector (tla.max 1 (tla.size G))) 0 (tla.size G)))

(define tla.eval-pred-h
  G P V I N -> V where (= I N)
  G P V I N -> (do (address-> V I (P (tla.state G I))) (tla.eval-pred-h G P V (+ I 1) N)))

(define tla.range
  I N -> [] where (>= I N)
  I N -> [I | (tla.range (+ I 1) N)])

(define tla.temporal
  G Opts -> (let Fair (append (map (/. E [wf (hd E) (tla.fun (hd (tl E)))]) (tla.entries wf Opts))
                              (map (/. E [sf (hd E) (tla.fun (hd (tl E)))]) (tla.entries sf Opts)))
                 Ev (tla.check-eventually G (tla.named eventually Opts) Fair)
              (if (tla.violation? Ev)
                  Ev
                  (let Lt (tla.check-leads-to G (tla.entries leads-to Opts) Fair)
                    (if (tla.violation? Lt)
                        Lt
                        (tla.check-possible G (tla.named possible Opts)))))))

(define tla.check-eventually
  _ [] _ -> ok
  G [[Name P] | Ps] Fair
  -> (let Goal (tla.eval-pred G P)
          Sources (tla.filter (/. I (and (= (tla.parent G I) -1) (not (<-address Goal I))))
                          (tla.range 0 (tla.size G)))
          V (tla.liveness G Sources Goal Fair)
        (if (tla.violation? V)
            (tla.name-violation V Name)
            (tla.check-eventually G Ps Fair))))

(define tla.check-leads-to
  _ [] _ -> ok
  G [[Name P Q] | Ps] Fair
  -> (let PV (tla.eval-pred G (tla.fun P))
          Goal (tla.eval-pred G (tla.fun Q))
          Sources (tla.filter (/. I (and (<-address PV I) (not (<-address Goal I))))
                          (tla.range 0 (tla.size G)))
          V (tla.liveness G Sources Goal Fair)
        (if (tla.violation? V)
            (tla.name-violation V Name)
            (tla.check-leads-to G Ps Fair))))

(define tla.name-violation
  [violation Kind _ Trace Loop] Name -> [violation Kind Name Trace Loop])

(define tla.filter
  _ [] -> []
  F [X | Xs] -> (if (F X) [X | (tla.filter F Xs)] (tla.filter F Xs)))

(define tla.max
  A B -> (if (> A B) A B))

\\ AG EF P: every reachable state can still reach a P state.
(define tla.check-possible
  _ [] -> ok
  G [[Name P] | Ps]
  -> (let Goal (tla.eval-pred G P)
          Can (tla.can-reach G Goal)
          Bad (tla.first-false Can 0 (tla.size G))
        (if (= Bad -1)
            (tla.check-possible G Ps)
            [violation possibility Name (tla.path-to G Bad) -1])))

(define tla.first-false
  V I N -> -1 where (= I N)
  V I N -> I where (not (<-address V I))
  V I N -> (tla.first-false V (+ I 1) N))

(define tla.preds
  G -> (let N (tla.size G)
            P (tla.filled N [])
            Skip32 (tla.preds-h G P 0 N)
          P))

(define tla.preds-h
  G P I N -> P where (= I N)
  G P I N -> (do (tla.add-preds P I (tla.succs G I)) (tla.preds-h G P (+ I 1) N)))

(define tla.add-preds
  _ _ [] -> done
  P I [(@p _ J) | Es] -> (do (address-> P J [I | (<-address P J)]) (tla.add-preds P I Es)))

(define tla.can-reach
  G Goal -> (let N (tla.size G)
                 Can (tla.filled N false)
                 Start (tla.filter (/. I (<-address Goal I)) (tla.range 0 N))
                 Skip33 (map (/. I (address-> Can I true)) Start)
               (tla.back-bfs (tla.preds G) Can Start)))

(define tla.back-bfs
  _ Can [] -> Can
  P Can [V | Vs] -> (tla.back-bfs P Can (tla.mark-new Can (<-address P V) Vs)))

(define tla.mark-new
  _ [] Acc -> Acc
  Can [U | Us] Acc -> (if (<-address Can U)
                          (tla.mark-new Can Us Acc)
                          (do (address-> Can U true) (tla.mark-new Can Us [U | Acc]))))

\\ --- liveness: a fair behaviour from Sources that never meets Goal ---

\\ Region search: breadth first from Sources through non-goal nodes.
\\ From[i] = predecessor in the region (-1 for a source, -2 unvisited);
\\ Root[i] = the source it was reached from. Returns the visit order.
(define tla.region
  G Sources Goal From Root
  -> (tla.region-h G Goal From Root (tla.seed From Root Sources []) []))

(define tla.seed
  _ _ [] Acc -> (reverse Acc)
  From Root [S | Ss] Acc -> (if (= (<-address From S) -2)
                                (do (address-> From S -1) (address-> Root S S)
                                    (tla.seed From Root Ss [S | Acc]))
                                (tla.seed From Root Ss Acc)))

(define tla.region-h
  _ _ _ _ [] Order -> (reverse Order)
  G Goal From Root [U | Queue] Order
  -> (let New (tla.region-visit G Goal From Root U (tla.succs G U) [])
        (tla.region-h G Goal From Root (append Queue New) [U | Order])))

(define tla.region-visit
  _ _ _ _ _ [] Acc -> (reverse Acc)
  G Goal From Root U [(@p _ V) | Es] Acc
  -> (if (and (= (<-address From V) -2) (not (<-address Goal V)))
         (do (address-> From V U) (address-> Root V (<-address Root U))
             (tla.region-visit G Goal From Root U Es [V | Acc]))
         (tla.region-visit G Goal From Root U Es Acc)))

(define tla.liveness
  G Sources Goal Fair
  -> (let N (tla.size G)
          From (tla.filled N -2)
          Root (tla.filled N -1)
          Order (tla.region G Sources Goal From Root)
          Terminal (tla.find (/. U (empty? (tla.succs G U))) Order)
        (if (= Terminal none)
            (tla.liveness-cycles G Order From Root Fair)
            [violation liveness none (tla.prefix G From Root Terminal) -1])))

(define tla.find
  _ [] -> none
  F [X | Xs] -> (if (F X) X (tla.find F Xs)))

\\ Path from an initial state to U: shortest to its source, then the
\\ region path from the source.
(define tla.prefix
  G From Root U -> (append (tla.path-to G (<-address Root U))
                           (tla.region-path G From U [])))

(define tla.region-path
  G From U Acc -> Acc where (= (<-address From U) -1)
  G From U Acc -> (let P (<-address From U)
                    (tla.region-path G From P [(tla.step-via G P U) | Acc])))

(define tla.step-via
  G U V -> (@p (tla.edge-action (tla.succs G U) V) (tla.state G V)))

(define tla.edge-action
  [(@p A V) | _] V -> A
  [_ | Es] V -> (tla.edge-action Es V))

(define tla.liveness-cycles
  G Order From Root Fair
  -> (let InRegion (tla.member-flags (tla.size G) Order)
          Comps (tla.sccs G Order (/. V (<-address InRegion V)))
        (tla.first-fair G Order From Root Fair Comps)))

(define tla.first-fair
  _ _ _ _ _ [] -> ok
  G Order From Root Fair [C | Cs]
  -> (let Comp (tla.fair-component G C Fair)
        (if (= Comp none)
            (tla.first-fair G Order From Root Fair Cs)
            (let In (tla.member-flags (tla.size G) Comp)
                 Entry (tla.find (/. U (<-address In U)) Order)
                 Prefix (tla.prefix G From Root Entry)
               [violation liveness none
                (append Prefix (tla.fair-cycle G Entry In Comp Fair))
                (- (length Prefix) 1)]))))

(define tla.member-flags
  N Nodes -> (let F (tla.filled N false)
                  Skip34 (map (/. U (address-> F U true)) Nodes)
                F))

\\ The part of component C in which a behaviour can cycle forever while
\\ meeting every fairness constraint, or none.
(define tla.fair-component
  G C Fair -> (let In (tla.member-flags (tla.size G) C)
                (if (tla.cyclic? G C In)
                    (tla.fair-h G C In Fair Fair)
                    none)))

\\ Checks the constraints Fs one by one; All is the whole list, needed
\\ again when strong fairness narrows the component and starts over.
(define tla.fair-h
  _ C _ [] _ -> C
  G C In [[Kind _ Sel] | Fs] All
  -> (if (tla.taken-within? G C In Sel)
         (tla.fair-h G C In Fs All)
         (let Disabled (tla.filter (/. U (not (tla.enables? G U Sel))) C)
           (cases (= (length Disabled) (length C)) (tla.fair-h G C In Fs All)  \\ never enabled here
                  (= Kind wf) (if (empty? Disabled) none (tla.fair-h G C In Fs All))
                  true (let DIn (tla.member-flags (tla.size G) Disabled)
                         (tla.first-some (/. D (tla.fair-component G D All))
                                         (tla.sccs G Disabled (/. V (<-address DIn V)))))))))

(define tla.first-some
  _ [] -> none
  F [X | Xs] -> (let R (F X) (if (= R none) (tla.first-some F Xs) R)))

(define tla.cyclic?
  G C In -> (not (= none (tla.find (/. U (tla.edge-into? (tla.succs G U) In)) C))))

(define tla.edge-into?
  [] _ -> false
  [(@p _ V) | Es] In -> (or (<-address In V) (tla.edge-into? Es In)))

(define tla.enables?
  G U Sel -> (not (= none (tla.find (/. E (Sel (fst E))) (tla.succs G U)))))

(define tla.taken-within?
  G C In Sel -> (not (= none (tla.internal-edge G C In Sel))))

\\ An internal edge (@p U V) whose action satisfies Sel, or none.
(define tla.internal-edge
  _ [] _ _ -> none
  G [U | Us] In Sel -> (let E (tla.find (/. E (and (Sel (fst E)) (<-address In (snd E))))
                                        (tla.succs G U))
                         (if (= E none) (tla.internal-edge G Us In Sel) (@p U (snd E)))))

\\ A cycle from Entry back to Entry inside the component that takes, or
\\ visits a state disabling, each fairness-constrained action. Returns
\\ the steps after Entry, without the final return to Entry.
(define tla.fair-cycle
  G Entry In C Fair -> (let R (tla.cycle-goals G In C Fair Entry [] false)
                            At (fst R)
                            Steps (snd R)
                            Moved (if (empty? Steps) (tla.first-step G Entry In) Steps)
                            At2 (if (empty? Steps) (tla.step-target G Moved) At)
                            Back (tla.walk G At2 Entry In)
                            All (append Moved Back)
                          (tla.drop-last All)))

(define tla.cycle-goals
  _ _ _ [] At Steps _ -> (@p At Steps)
  G In C [[_ _ Sel] | Fs] At Steps Moved
  -> (let E (tla.internal-edge G C In Sel)
        (if (= E none)
            (let D (tla.find (/. U (not (tla.enables? G U Sel))) C)
              (if (or (= D none) (= D At))
                  (tla.cycle-goals G In C Fs At Steps Moved)
                  (tla.cycle-goals G In C Fs D (append Steps (tla.walk G At D In)) true)))
            (let U (fst E)
                 V (snd E)
               (tla.cycle-goals G In C Fs V
                                (append Steps (append (tla.walk G At U In) [(tla.step-via G U V)]))
                                true)))))

(define tla.first-step
  G Entry In -> (let E (tla.find (/. E (<-address In (snd E))) (tla.succs G Entry))
                  [(tla.step-via G Entry (snd E))]))

\\ The node a list of steps ends at, found by its state.
(define tla.step-target
  G Steps -> (tla.map-get (<-address G 5) (snd (hd (reverse Steps)))))

(define tla.drop-last
  [] -> []
  [_] -> []
  [X | Xs] -> [X | (tla.drop-last Xs)])

\\ Shortest path of steps from U to V (U /= V) inside In.
(define tla.walk
  _ U U _ -> []
  G U V In -> (let Prev (tla.filled (tla.size G) -2)
                   Skip35 (address-> Prev U -1)
                 (tla.walk-bfs G V In Prev [U])))

(define tla.walk-bfs
  G V In Prev [] -> (simple-error "tla: no path inside component")
  G V In Prev [X | Queue]
  -> (let New (tla.walk-visit G V In Prev X (tla.succs G X) [])
        (if (= (<-address Prev V) -2)
            (tla.walk-bfs G V In Prev (append Queue New))
            (tla.walk-path G Prev V []))))

(define tla.walk-visit
  _ _ _ _ _ [] Acc -> (reverse Acc)
  G V In Prev X [(@p _ Y) | Es] Acc
  -> (if (and (<-address In Y) (= (<-address Prev Y) -2))
         (do (address-> Prev Y X) (tla.walk-visit G V In Prev X Es [Y | Acc]))
         (tla.walk-visit G V In Prev X Es Acc)))

(define tla.walk-path
  G Prev V Acc -> Acc where (= (<-address Prev V) -1)
  G Prev V Acc -> (let P (<-address Prev V)
                    (tla.walk-path G Prev P [(tla.step-via G P V) | Acc])))

\\ --- strongly connected components (Kosaraju, iterative) ---

(define tla.sccs
  G Nodes InSet -> (let N (tla.size G)
                        Seen (tla.filled N false)
                        Finish (tla.dfs-all G Nodes InSet Seen [])
                        Preds (tla.preds G)
                        Done (tla.filled N false)
                      (tla.collect G Finish InSet Preds Done [])))

\\ Nodes in order of DFS finish, last finished first.
(define tla.dfs-all
  _ [] _ _ Finish -> Finish
  G [U | Us] InSet Seen Finish
  -> (if (<-address Seen U)
         (tla.dfs-all G Us InSet Seen Finish)
         (do (address-> Seen U true)
             (tla.dfs-all G Us InSet Seen (tla.dfs G InSet Seen [(@p U (tla.succs G U))] Finish)))))

\\ The stack holds (@p Node RemainingEdges).
(define tla.dfs
  _ _ _ [] Finish -> Finish
  G InSet Seen [(@p U []) | Stack] Finish -> (tla.dfs G InSet Seen Stack [U | Finish])
  G InSet Seen [(@p U [(@p _ V) | Es]) | Stack] Finish
  -> (if (and (InSet V) (not (<-address Seen V)))
         (do (address-> Seen V true)
             (tla.dfs G InSet Seen [(@p V (tla.succs G V)) (@p U Es) | Stack] Finish))
         (tla.dfs G InSet Seen [(@p U Es) | Stack] Finish)))

(define tla.collect
  _ [] _ _ _ Comps -> Comps
  G [U | Us] InSet Preds Done Comps
  -> (if (<-address Done U)
         (tla.collect G Us InSet Preds Done Comps)
         (do (address-> Done U true)
             (tla.collect G Us InSet Preds Done
                          [(tla.back-dfs InSet Preds Done [U] []) | Comps]))))

(define tla.back-dfs
  _ _ _ [] Comp -> Comp
  InSet Preds Done [U | Stack] Comp
  -> (tla.back-dfs InSet Preds Done
                   (tla.push-preds InSet Done (<-address Preds U) Stack)
                   [U | Comp]))

(define tla.push-preds
  _ _ [] Stack -> Stack
  InSet Done [P | Ps] Stack -> (if (and (InSet P) (not (<-address Done P)))
                                   (do (address-> Done P true)
                                       (tla.push-preds InSet Done Ps [P | Stack]))
                                   (tla.push-preds InSet Done Ps Stack)))

\\ ---------------------------------------------------------------------
\\ Trace validation

(define tla.validate
  Init Next Opts Trace
  -> (let Steps (map (fn tla.label-trace) Trace)
          NextF (tla.fun Next)
          Invs (tla.named invariant Opts)
          StepInvs (tla.named step-invariant Opts)
        (cases (empty? Steps) ok
               (and (not (tla.opt from-any Opts false))
                    (not (element? (snd (hd Steps)) Init)))
               [trace-failure 0 (make-string "~S is not an initial state" (snd (hd Steps)))
                (map (/. S (@p init S)) Init)]
               true (tla.validate-h NextF Invs StepInvs Steps none 0))))

(define tla.label-trace
  S -> S where (tuple? S)
  S -> (@p none S))

(define tla.validate-h
  _ _ _ [] _ _ -> ok
  Next Invs StepInvs [(@p A S) | Rest] Prev I
  -> (let Bad (tla.broken S Invs)
        (cases (not (= Bad none))
               [trace-failure I (make-string "invariant ~A violated by ~S" Bad S) []]
               (or (= I 0) (= S Prev))
               (tla.validate-h Next Invs StepInvs Rest S (+ I 1))
               true
               (let Allowed (map (fn tla.label) (Next Prev))
                 (if (tla.explains? Allowed A S)
                     (let BadStep (tla.broken-step Prev S StepInvs)
                       (if (= BadStep none)
                           (tla.validate-h Next Invs StepInvs Rest S (+ I 1))
                           [trace-failure I (make-string "step invariant ~A violated from ~S to ~S"
                                                         BadStep Prev S) []]))
                     [trace-failure I
                      (if (= A none)
                          (make-string "no action takes ~S to ~S" Prev S)
                          (make-string "action ~S does not take ~S to ~S" A Prev S))
                      Allowed])))))

(define tla.explains?
  [] _ _ -> false
  [(@p A2 T) | Rest] A S -> (if (and (= T S) (or (= A none) (= A A2)))
                                true
                                (tla.explains? Rest A S)))

\\ ---------------------------------------------------------------------
\\ Reporting

(define tla.report
  [ok N T D] -> (output "~A states, ~A transitions, depth ~A~%OK~%" N T D)
  [incomplete N T D] -> (output "~A states, ~A transitions, depth ~A~%INCOMPLETE: max-states reached~%" N T D)
  [violation Kind Name Trace Loop N T D]
  -> (do (output "~A states, ~A transitions, depth ~A~%FAIL: ~A ~A~%" N T D Kind Name)
         (tla.print-trace Trace 0)
         (cases (not (= Kind liveness)) ok
                (= Loop -1) (output "       (stays here forever: no action is enabled)~%")
                true (output "       (back to state ~A, and repeats forever)~%" Loop)))
  ok -> (output "OK~%")
  [trace-failure I Reason _] -> (output "FAIL: trace step ~A: ~A~%" I Reason))

(define tla.print-trace
  [] _ -> ok
  [(@p A S) | Rest] I -> (do (output "  ~A  ~A  ~S~%" I A S) (tla.print-trace Rest (+ I 1))))
