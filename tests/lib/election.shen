\* Leader election, the running example of Reasonable's "The internet
   discovers TLA+. Now what?", as a tla.shen spec.

   A state is [Roles Votes]: Roles lists each computer's role (follower,
   candidate or leader); Votes lists, per computer, who it has voted for.

   Knobs (set before checking, like a TLC model's CONSTANTS):
     *computers*    the computers, e.g. [a b c]
     *quorum*       votes needed to win
     *double-vote*  the bug: a computer may vote for a second candidate
     *timeouts*     a split vote is abandoned and the election restarts *\

(set *computers* [a b c])
(set *quorum* 2)
(set *double-vote* false)
(set *timeouts* false)

(define election.init
  -> [[(map (/. C follower) (value *computers*))
       (map (/. C []) (value *computers*))]])

\\ The role or votes of computer C, and replacing them.
(define election.of
  C [C2 | Cs] [X | Xs] -> (if (= C C2) X (election.of C Cs Xs)))

(define election.with
  C V [C2 | Cs] [X | Xs] -> (if (= C C2) [V | Xs] [X | (election.with C V Cs Xs)]))

(define election.role C Roles -> (election.of C (value *computers*) Roles))
(define election.votes C Votes -> (election.of C (value *computers*) Votes))
(define election.set C V L -> (election.with C V (value *computers*) L))

(define election.tally
  C Votes -> (length (election.filter (/. Vs (element? C Vs)) Votes)))

(define election.filter
  _ [] -> []
  F [X | Xs] -> (if (F X) [X | (election.filter F Xs)] (election.filter F Xs)))

\\ Add a vote, keeping each list in *computers* order so that equal
\\ vote sets are equal states.
(define election.add
  J Vs -> (election.filter (/. C (or (= C J) (element? C Vs))) (value *computers*)))

(define election.may-vote?
  I J Votes -> (let Mine (election.votes I Votes)
                 (if (value *double-vote*)
                     (not (element? J Mine))
                     (empty? Mine))))

\\ Actions. Each returns a list of (@p Action State).
(define election.start
  I [Roles Votes] -> [(@p [start I] [(election.set I candidate Roles)
                                     (election.set I [I] Votes)])]
                     where (and (= (election.role I Roles) follower)
                                (empty? (election.votes I Votes)))
  _ _ -> [])

(define election.vote
  I J [Roles Votes] -> [(@p [vote I J] [Roles (election.set I (election.add J (election.votes I Votes)) Votes)])]
                       where (and (not (= I J))
                                  (and (= (election.role J Roles) candidate)
                                       (election.may-vote? I J Votes)))
  _ _ _ -> [])

(define election.win
  I [Roles Votes] -> [(@p [win I] [(election.set I leader Roles) Votes])]
                     where (and (= (election.role I Roles) candidate)
                                (>= (election.tally I Votes) (value *quorum*)))
  _ _ -> [])

(define election.timeout
  [Roles Votes] -> (map (/. S (@p timeout S)) (election.init))
                   where (and (value *timeouts*)
                              (and (not (element? [] Votes))
                                   (empty? (election.for-each (/. I (election.win I [Roles Votes]))))))
  _ -> [])

(define election.for-each
  F -> (mapcan F (value *computers*)))

(define election.next
  S -> (append (election.for-each (/. I (election.start I S)))
               (append (election.for-each (/. I (election.for-each (/. J (election.vote I J S)))))
                       (append (election.for-each (/. I (election.win I S)))
                               (election.timeout S)))))

\\ Properties.
(define election.one-leader? [Roles _] -> (<= (occurrences leader Roles) 1))
(define election.has-leader? [Roles _] -> (element? leader Roles))
(define election.can-start?
  S -> (not (empty? (election.for-each (/. I (election.start I S))))))

\\ Fairness selectors: actions are values, so fairness is a predicate.
(define election.a-vote?
  [vote _ _] -> true
  _ -> false)
