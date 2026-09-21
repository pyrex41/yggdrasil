\\ Fixture for the stage-2 initialisation-order check (docs/analysis-rules.md).
\\ This program is REFUSED on purpose: its first toplevel form reads the
\\ global *greeting* while the form that sets it comes on the next line, so
\\ at boot the read runs against an unset global.
\\
\\ Expected: the shake fails, writes no kernel.kl, and prints the sentinel
\\   yggdrasil-shake: FAIL init-order form=N reads=*greeting*
\\ (N is the form's 1-based index in the final sequence: the kernel's own
\\ init forms come first, so N is large.)  There is deliberately no
\\ tests/init-order-bad.expected: the parity gate only runs fixtures that
\\ have a committed golden beside them, which keeps this one out of it.
\\ tests/init-order-ok.shen is the same program in the right order.

(output "~A~%" (value *greeting*))

(set *greeting* "hello from an initialised global")
