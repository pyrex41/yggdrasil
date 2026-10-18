#!/usr/bin/env python3
"""Adapt Yggdrasil's manifest arities to Shen-C's native KL compiler."""
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile


def compile_manifest(builder, source, output):
    manifest = source / "yggdrasil.manifest.txt"
    if not manifest.is_file():
        return subprocess.call([builder, str(source), str(output)])
    lines = manifest.read_text().splitlines()
    declarations = []
    for line in lines:
        if not line.startswith("fn="):
            continue
        fields = line[3:].rsplit(None, 1)
        if len(fields) != 2 or not re.fullmatch(r'[^\s()"]+', fields[0]):
            raise ValueError("invalid fn= declaration: " + line)
        name, arity = fields
        if not arity.isascii() or not arity.isdigit() or int(arity) > 2147483647:
            raise ValueError("invalid fn= arity: " + line)
        declarations.append("(put %s arity %d (value *property-vector*))\n" % (name, int(arity)))
    if not declarations:
        return subprocess.call([builder, str(source), str(output)])

    # The kernel initializes *property-vector* before user forms. Its retained
    # arity-table initialization already keeps put reachable. Register declared
    # user arities next, without requiring the optional source-reflection helpers
    # that the shaker can remove from a small artifact.
    with tempfile.TemporaryDirectory(prefix="yggdrasil-c-rc-") as temporary:
        stage = pathlib.Path(temporary)
        (stage / "arities.kl").write_text("".join(declarations))
        rewritten = []
        number = 0
        kernel = "kernel.kl"
        for line in lines:
            key, separator, value = line.partition("=")
            if separator and key == "kernel":
                kernel = value
                continue
            if separator and key in ("user", "source-events"):
                name = "input-%d.kl" % number
                number += 1
                shutil.copyfile(source / value, stage / name)
                rewritten.append(key + "=" + name)
            else:
                rewritten.append(line)
        shutil.copyfile(source / kernel, stage / "kernel.kl")
        (stage / "yggdrasil.manifest.txt").write_text(
            "kernel=kernel.kl\nuser=arities.kl\n" + "\n".join(rewritten) + "\n")
        return subprocess.call([builder, str(stage), str(output)])


if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.exit("usage: compile.py RC_BUILDER SHAKEN_DIR OUTPUT_DIR")
    try:
        sys.exit(compile_manifest(sys.argv[1], pathlib.Path(sys.argv[2]), pathlib.Path(sys.argv[3])))
    except (OSError, ValueError) as error:
        sys.exit("c: " + str(error))
