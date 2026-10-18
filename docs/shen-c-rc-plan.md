# Shen-C native RC integration

Base: `3ac17621`. Work is isolated on `fix/shen-c-native-rc`.

1. Replace the removed Boehm/libshenc builder with the released `rc-build`.
   Preserve manifest-selected kernel/user files, explicit init ordering and
   `fn=` declarations, adapting them to KL arity registration before user code.
2. Remove pkg-config/Boehm/Nix re-entry requirements; keep C17, make and the
   Python manifest tool. Preserve `SHEN_C` and `YGGDRASIL_SHEN_C_DIR` overrides.
3. Verify a real native manifest build, then shake and run representative
   source programs with a genuine 42.2 stage-1 host. Run Go tests and audit
   native binary dependencies. Keep unavailable platform gates UNKNOWN.
4. Update the builder contract and qualification record. Yggdrasil's shared
   42.2 kernel and other builders remain the source of their existing behavior;
   this task updates the C target to Shen-C v0.3.0's compiler/runtime.
