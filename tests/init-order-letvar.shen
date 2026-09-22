\\ Fixture for the stage-2 initialisation-order check (docs/analysis-rules.md).
\\ The (value X) below reads a LET VARIABLE, not a global: after compilation
\\ the toplevel form is (let X *a* (print (value X))), and X is a KL variable
\\ bound to the symbol *a*.
\\
\\ A KL variable is a symbol, so a check that guards on symbol? alone reports
\\ this as a read of an unwritten global named X.  It is not one: it is a
\\ COMPUTED name, which is stage 3's business (ygg.cn-global?, the
\\ computed-names= manifest key), and stage 2 has nothing true to say about
\\ it.  The shake must accept the program.
\\
\\ There is deliberately no tests/init-order-letvar.expected: the parity gate
\\ only runs fixtures with a committed golden beside them.  Running it prints
\\ 1.

(set *a* 1)

(let X *a* (print (value X)))
