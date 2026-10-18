;;; Yggdrasil - stage-2 Common Lisp driver (SBCL / CLISP / ECL).
;;;
;;; Run with the staged directory as the working directory:
;;;     sbcl  --non-interactive --no-userinit --no-sysinit --load driver.lsp
;;;     clisp -norc -q driver.lsp
;;;     ecl   --norc --load driver.lsp
;;;
;;; Expects alongside this file (staged by builders/lisp/build.shen):
;;;     package.lsp primitives.lsp native.lsp shen-utils.lsp overwrite.lsp
;;;     dictionaries.lsp (Shen CL 3.1+ runtimes)
;;;     kernel.lsp <user>.lsp yggdrasil.config.lsp
;;;     compiler.lsp (eval-capable builds only: manifest needs-eval=true)
;;;
;;; Mirrors the minimal subset of shen-cl's boot.lsp:
;;;     package -> primitives -> native -> shen-utils -> kernel ->
;;;     overwrite -> (shen.initialise) -> user modules -> executable.
;;;
;;; SBCL and CLISP capture all of that in a saved image whose toplevel runs
;;; the user code.  ECL cannot dump images: it compiles every module to an
;;; object file and links a real executable with c:build-program, so the
;;; load->overwrite->initialise->user sequence runs at program startup
;;; instead (see the epilogue).  shen-cl's runtime .lsp sources and the
;;; kl->lisp output are implementation-portable; only this driver varies.
;;; CCL is unsupported here: no native Apple Silicon build exists.

(in-package :cl-user)

(setq *load-verbose* nil
      *load-print* nil
      *compile-verbose* nil
      *compile-print* nil)

#+clisp (setq custom:*compile-warnings* nil
              custom:*suppress-check-redefinition* t)
#+ecl   (require 'cmp)
#+ecl   (setq c::*suppress-compiler-warnings* t
              c::*suppress-compiler-notes* t)

(load "package.lsp")

(in-package :shen)

(proclaim '(optimize (debug 0) (speed 3) (safety 3)))
#+sbcl (declaim (sb-ext:muffle-conditions warning sb-ext:compiler-note))
#+sbcl (setf sb-ext:*muffled-warnings* t)

;; Build parameters: yggdrasil-user-names (user module base names, in
;; manifest order) and yggdrasil-exe (path for the saved executable).
(load "yggdrasil.config.lsp")

(defun yggdrasil-exit (code)
  #+sbcl  (sb-ext:exit :code code :abort (/= code 0))
  #+clisp (ext:exit code)
  #+ecl   (si:quit code))

;; overwrite.lsp patches kernel functions with platform-native versions and
;; must apply after the kernel (boot.lsp order), before (shen.initialise).
;; A shaken kernel omits functions the program never reaches, and a few
;; overwrite forms grab their patch target at load time (fdefinition / #'),
;; so eval form by form and skip patches whose kernel target is absent.
;; A skipped patch is sound exactly when its target lies outside the
;; shaken footprint.
(defun yggdrasil-load-overwrite (file)
  (with-open-file (in file)
    (loop with eof = (list :eof)
          for form = (read in nil eof)
          until (eq form eof)
          do (handler-case (eval form)
               (error (c)
                 (format t "~&;; yggdrasil: skipped overwrite form (~{~S~^ ~} ...): ~A~%"
                         (if (consp form)
                             (subseq form 0 (min 2 (length form)))
                             (list form))
                         c))))))

;; shen-cl's native pr override is #+(or ccl sbcl); elsewhere the kernel's
;; KL pr runs and needs the optional stream primitives (the manifest's
;; primitive-optional= pair plus the char-stream predicates).  Installed
;; only when missing, after overwrite.lsp has had its chance.
(defun yggdrasil-install-stream-fallbacks ()
  (unless (fboundp '|shen.char-stoutput?|)
    (setf (symbol-function '|shen.char-stoutput?|)
          (lambda (s)
            (if (subtypep (stream-element-type s) 'character) '|true| '|false|))))
  (unless (fboundp '|shen.char-stinput?|)
    (setf (symbol-function '|shen.char-stinput?|)
          (lambda (s)
            (if (subtypep (stream-element-type s) 'character) '|true| '|false|))))
  (unless (fboundp '|shen.write-string|)
    (setf (symbol-function '|shen.write-string|)
          (lambda (x s)
            (write-string x s)
            (force-output s)
            x)))
  (unless (fboundp '|shen.read-unit-string|)
    (setf (symbol-function '|shen.read-unit-string|)
          (lambda (s)
            (let ((c (read-char s nil nil)))
              (if c (string c) ""))))))

;; Run each user module's |yggdrasil.toplevel-<name>| (its toplevel
;; non-defun forms, in source order), in manifest order; then exit.
(defun yggdrasil-toplevel ()
  (let ((*package* (find-package :shen)))
    ;; Streams captured at image-save time may be dead on restart (CLISP
    ;; keeps the closed object; SBCL transparently revives fd streams).
    (setq |*stoutput*| *standard-output*
          |*stinput*|  *standard-input*)
    (when (boundp '|*sterror*|)
      (setq |*sterror*| *error-output*))
    (handler-case
        (progn
          (dolist (name yggdrasil-user-names)
            (funcall
             (or (find-symbol (concatenate 'string "yggdrasil.toplevel-" name)
                              :shen)
                 (error "yggdrasil: missing toplevel function for ~A" name))))
          (force-output |*stoutput*|)
          (yggdrasil-exit 0))
      (error (c)
        (format *error-output* "~&yggdrasil: uncaught error: ~A~%" c)
        (force-output *error-output*)
        (yggdrasil-exit 1)))))

#-ecl
(progn
  (defun yggdrasil-import (file)
    (load (compile-file file :verbose nil :print nil #+sbcl :block-compile #+sbcl nil)))

  (yggdrasil-import "primitives.lsp")
  (when (probe-file "dictionaries.lsp")
    (yggdrasil-import "dictionaries.lsp"))
  (yggdrasil-import "native.lsp")
  (yggdrasil-import "shen-utils.lsp")
  ;; Eval-capable programs ship shen-cl's KL->Lisp compiler so the eval-kl
  ;; primitive works at runtime; shen-cl's boot loads it in this position.
  ;; initialise-compiler only sets the compiler's two flag globals.
  (when yggdrasil-needs-eval
    (yggdrasil-import "compiler.lsp")
    (|shen-cl.initialise-compiler|))
  (yggdrasil-import "kernel.lsp")
  (yggdrasil-load-overwrite "overwrite.lsp")
  (yggdrasil-install-stream-fallbacks)

  ;; Builder contract: load kernel, call (shen.initialise), load user code.
  ;; Image-based implementations initialise at build time and capture the
  ;; result in the image, exactly as shen-cl's own boot does.
  (|shen.initialise|)

  (dolist (name yggdrasil-user-names)
    (yggdrasil-import (concatenate 'string name ".lsp"))))

;; YGGDRASIL_SBCL_COMPRESSION=1 (any value but empty or 0) saves the image
;; with :compression t, which needs an SBCL built with :sb-core-compression
;; (zstd or zlib core compression); without that feature the build fails
;; here, by name, rather than producing an uncompressed image silently.
;; The image is several times smaller and starts measurably slower.
#+sbcl
(let* ((env (sb-ext:posix-getenv "YGGDRASIL_SBCL_COMPRESSION"))
       (compress (and env (string/= env "") (string/= env "0"))))
  (when (and compress (not (member :sb-core-compression *features*)))
    (format *error-output* "~&yggdrasil: YGGDRASIL_SBCL_COMPRESSION is set but this SBCL was built without :sb-core-compression~%")
    (yggdrasil-exit 1))
  (apply #'sb-ext:save-lisp-and-die yggdrasil-exe
         :executable t
         :save-runtime-options t
         :toplevel #'yggdrasil-toplevel
         (when compress (list :compression t))))

#+clisp
(progn
  (ext:saveinitmem yggdrasil-exe
                   :executable t
                   :quiet t
                   :norc t
                   :init-function #'yggdrasil-toplevel)
  (ext:exit 0))

;; ECL compiles intra-file calls as direct C calls, so a kernel function
;; redefined by overwrite.lsp would keep its original definition at call
;; sites inside the file that defines it (e.g. shen.initialise's call to
;; shen.dict must reach the native hash-table dict, not the kernel's
;; absvector one).  Proclaim every function overwrite.lsp defines
;; NOTINLINE before compiling the modules, exactly as shen-cl's boot does.
#+ecl
(with-open-file (in "overwrite.lsp")
  (loop for form = (read in nil :eof)
        until (eq form :eof)
        when (and (consp form) (eq (car form) 'defun))
        do (proclaim (list 'notinline (cadr form)))))

;; ECL: compile every module to an object file and link an executable.
;; The epilogue replays the boot order at startup: overwrite patches
;; (interpreted form-by-form for the skip-on-missing-target behavior),
;; then initialise, then the user toplevels.  overwrite.lsp and
;; yggdrasil.config.lsp therefore ship inside the binary's directory is
;; NOT needed - both are baked in: the config was already loaded above
;; (its defparameters compile into the prologue object), and the
;; overwrite source text is embedded as a literal.
#+ecl
(let* ((modules (append '("package" "yggdrasil.config"
                          "primitives")
                        (when (probe-file "dictionaries.lsp") '("dictionaries"))
                        '("native" "shen-utils")
                        (when yggdrasil-needs-eval '("compiler"))
                        '("kernel")
                        yggdrasil-user-names))
       (overwrite-text
        (with-open-file (in "overwrite.lsp")
          (let ((s (make-string (file-length in))))
            (subseq s 0 (read-sequence s in)))))
       ;; The epilogue must be self-contained: build-program links only the
       ;; compiled modules plus this form, so nothing defined in THIS driver
       ;; process (which exits after linking) can be referenced from it.
       (epilogue
        `(progn
           (in-package :shen)
           ,@(when yggdrasil-needs-eval '((|shen-cl.initialise-compiler|)))
           (with-input-from-string (in ,overwrite-text)
             (loop with eof = (list :eof)
                   for form = (read in nil eof)
                   until (eq form eof)
                   do (handler-case (eval form)
                        (error (c) (declare (ignore c)) nil))))
           (unless (fboundp '|shen.char-stoutput?|)
             (setf (symbol-function '|shen.char-stoutput?|)
                   (lambda (s) (if (subtypep (stream-element-type s) 'character) '|true| '|false|))))
           (unless (fboundp '|shen.char-stinput?|)
             (setf (symbol-function '|shen.char-stinput?|)
                   (lambda (s) (if (subtypep (stream-element-type s) 'character) '|true| '|false|))))
           (unless (fboundp '|shen.write-string|)
             (setf (symbol-function '|shen.write-string|)
                   (lambda (x s) (write-string x s) (force-output s) x)))
           (unless (fboundp '|shen.read-unit-string|)
             (setf (symbol-function '|shen.read-unit-string|)
                   (lambda (s) (let ((c (read-char s nil nil))) (if c (string c) "")))))
           (|shen.initialise|)
           (handler-case
               (progn
                 (dolist (name ',yggdrasil-user-names)
                   (funcall
                    (or (find-symbol (concatenate 'string "yggdrasil.toplevel-" name) :shen)
                        (error "yggdrasil: missing toplevel function for ~A" name))))
                 (force-output |*stoutput*|)
                 (si:quit 0))
             (error (c)
               (format *error-output* "~&yggdrasil: uncaught error: ~A~%" c)
               (si:quit 1)))))
       (objects
        (mapcar (lambda (name)
                  (compile-file (concatenate 'string name ".lsp")
                                :system-p t :verbose nil :print nil))
                modules)))
  (c:build-program yggdrasil-exe
                   :lisp-files objects
                   :epilogue-code epilogue)
  (si:quit 0))
