#!/usr/bin/env python3
"""Lower Yggdrasil user KLambda to shen-joy's bounded normalized form."""

from __future__ import annotations

import pathlib
import sys


class String:
    def __init__(self, value: str) -> None:
        self.value = value


def parse_all(source: str):
    tokens = []
    i = 0
    while i < len(source):
        c = source[i]
        if c.isspace():
            i += 1
        elif c == "\\":
            i = source.find("\n", i)
            if i < 0:
                break
        elif c in "()":
            tokens.append(c)
            i += 1
        elif c == '"':
            i += 1
            out = []
            while i < len(source) and source[i] != '"':
                if source[i] == "\\" and i + 1 < len(source):
                    escapes = {"n": "\n", "r": "\r", "t": "\t"}
                    i += 1
                    out.append(escapes.get(source[i], source[i]))
                else:
                    out.append(source[i])
                i += 1
            if i >= len(source):
                raise ValueError("unterminated string")
            tokens.append(String("".join(out)))
            i += 1
        else:
            start = i
            while i < len(source) and not source[i].isspace() and source[i] not in "()":
                i += 1
            tokens.append(source[start:i])

    def one(at):
        if at >= len(tokens):
            raise ValueError("unexpected end of input")
        token = tokens[at]
        if token == "(":
            values = []
            at += 1
            while at < len(tokens) and tokens[at] != ")":
                value, at = one(at)
                values.append(value)
            if at >= len(tokens):
                raise ValueError("unclosed list")
            return values, at + 1
        if token == ")":
            raise ValueError("unexpected close parenthesis")
        return token, at + 1

    forms = []
    at = 0
    while at < len(tokens):
        form, at = one(at)
        forms.append(form)
    return forms


def atom(value) -> str:
    if isinstance(value, str):
        return value
    raise ValueError("string literals are not values in the bounded target")


def lower(expr) -> str:
    if isinstance(expr, str):
        return expr
    if not isinstance(expr, list) or not expr:
        raise ValueError("empty or malformed expression")
    head = atom(expr[0])
    if head == "cond":
        if len(expr) < 2:
            raise ValueError("empty cond")
        result = lower(expr[-1][1])
        if atom(expr[-1][0]) != "true":
            result = f"(if {lower(expr[-1][0])} {result} false)"
        for clause in reversed(expr[1:-1]):
            if not isinstance(clause, list) or len(clause) != 2:
                raise ValueError("malformed cond clause")
            result = f"(if {lower(clause[0])} {lower(clause[1])} {result})"
        return result
    if head in {"if", "let"}:
        expected = 4
        if len(expr) != expected:
            raise ValueError(f"{head} expects {expected - 1} arguments")
        return "(" + " ".join([head] + [lower(x) for x in expr[1:]]) + ")"
    if head == "do":
        return "(" + " ".join(["do"] + [lower(x) for x in expr[1:]]) + ")"
    if head in {"+", "-", "*", "=", "<", "cons"}:
        if len(expr) != 3:
            raise ValueError(f"{head} expects two arguments")
        return f"({head} {lower(expr[1])} {lower(expr[2])})"
    if head == ">":
        return f"(< {lower(expr[2])} {lower(expr[1])})"
    if head == "not":
        return f"(if {lower(expr[1])} false true)"
    if head in {"hd", "tl", "cons?"}:
        mapped = {"hd": "head", "tl": "tail", "cons?": "nil?"}[head]
        body = f"({mapped} {lower(expr[1])})"
        return f"(if {body} false true)" if head == "cons?" else body
    forbidden = {
        "eval", "eval-kl", "lambda", "freeze", "trap-error", "set", "value",
        "open", "close", "read-byte", "write-byte", "intern", "absvector",
    }
    if head in forbidden or head.startswith("shen."):
        raise ValueError(f"unsupported KLambda construct: {head}")
    return "(" + " ".join([head] + [lower(x) for x in expr[1:]]) + ")"


def output_value(form):
    # Exact lowering of: (output "~A~%" VALUE). Yggdrasil emits
    # (pr (shen.app VALUE "\\n" shen.a) (stoutput)).
    if not isinstance(form, list) or len(form) != 3 or form[0] != "pr":
        return None
    app = form[1]
    if (
        isinstance(app, list)
        and len(app) == 4
        and app[0] == "shen.app"
        and isinstance(app[2], String)
        and app[2].value == "\n"
        and app[3] == "shen.a"
    ):
        return app[1]
    raise ValueError('the joy target only supports top-level (output "~A~%" VALUE)')


def user_files(outdir: pathlib.Path):
    manifest = outdir / "yggdrasil.manifest.txt"
    names = []
    for line in manifest.read_text().splitlines():
        if line.startswith("user="):
            names.append(line.split("=", 1)[1])
    if not names:
        raise ValueError("manifest contains no user KLambda files")
    return [outdir / name for name in names]


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: lower.py OUTDIR OUTPUT.sjk", file=sys.stderr)
        return 2
    outdir, output = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
    functions = []
    entry = None
    try:
        for path in user_files(outdir):
            for form in parse_all(path.read_text()):
                if isinstance(form, list) and form and form[0] == "defun":
                    if len(form) != 4 or not isinstance(form[2], list):
                        raise ValueError("malformed defun")
                    name = atom(form[1])
                    params = " ".join(atom(x) for x in form[2])
                    functions.append(f"  (function {name} ({params}) {lower(form[3])})")
                else:
                    value = output_value(form)
                    if value is not None:
                        if entry is not None:
                            raise ValueError("multiple top-level output forms are unsupported")
                        entry = lower(value)
        if entry is None:
            raise ValueError('missing top-level (output "~A~%" VALUE)')
        functions.append(f"  (function yggdrasil-main () {entry})")
        text = "(program\n" + "\n".join(functions) + "\n  (entry yggdrasil-main))\n"
        output.write_text(text)
    except ValueError as error:
        print(f"yggdrasil joy target: {error}", file=sys.stderr)
        return 3
    except OSError as error:
        print(f"yggdrasil joy target: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
