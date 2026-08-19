#!/usr/bin/env python3
"""Tokenizer glue for scripts/check_mutation.py.

Shells out to a small embedded go/scanner helper program, run through
`go run`, and turns its token stream into mutation-candidate Sites.
Stdlib-only plus the go tool; never a third-party mutation library.
Ported from mivia-ai-sdk's scripts/mutation_tokenize.py, kept
line-for-line compatible so both repos stay easy to diff against each
other.
"""
from __future__ import annotations

import json
import subprocess
import tempfile
from pathlib import Path


class MutationError(Exception):
    """MutationError reports a problem the run must fail loudly on."""


# Operator mutations: matched token text -> replacement text. Both
# directions of each pair are separate entries so `==` and `!=` (and
# `<`/`<=`, `&&`/`||`) are each independent mutation sites.
OPERATOR_MUTATIONS = {
    "==": "!=",
    "!=": "==",
    "<": "<=",
    "<=": "<",
    "&&": "||",
    "||": "&&",
}

# TOKENIZER_SRC is a standalone go/scanner program written to a temp
# file and run with `go run`. It emits, as JSON, only the tokens whose
# text matches a mutation candidate: byte offsets, not a line number,
# since offsets survive unrelated edits above the site.
TOKENIZER_SRC = '''package main

import (
	"encoding/json"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
)

type site struct {
	Pos int    `json:"pos"`
	End int    `json:"end"`
	Tok string `json:"tok"`
}

var wanted = map[string]bool{
	"==": true, "!=": true, "<": true, "<=": true,
	"&&": true, "||": true, "!": true, "continue": true,
}

func main() {
	// go run passes "--" as the first argument, then the target file:
	// the "--" stops go run from treating the target as a second
	// source file of this helper's own package.
	if len(os.Args) != 3 || os.Args[1] != "--" {
		fmt.Fprintln(os.Stderr, "usage: scan -- <file>")
		os.Exit(1)
	}
	target := os.Args[2]
	src, err := os.ReadFile(target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fset := token.NewFileSet()
	file := fset.AddFile(target, fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	var sites []site
	for {
		pos, tk, _ := s.Scan()
		if tk == token.EOF {
			break
		}
		text := tk.String()
		if !wanted[text] {
			continue
		}
		offset := file.Offset(pos)
		sites = append(sites, site{Pos: offset, End: offset + len(text), Tok: text})
	}
	out, err := json.Marshal(sites)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}
'''


class Site:
    """Site is one candidate mutation: a file, a byte span, and the swap."""

    def __init__(self, path: Path, start: int, end: int, old: str, new: str, kind: str):
        self.path = path
        self.start = start
        self.end = end
        self.old = old
        self.new = new
        self.kind = kind

    def sort_key(self):
        return (str(self.path), self.start)


def run_go_tokenizer(path: Path) -> list[dict]:
    """run_go_tokenizer shells the embedded go/scanner helper over path."""
    with tempfile.TemporaryDirectory(prefix="mutation-scan-") as tmp:
        prog = Path(tmp) / "scan.go"
        prog.write_text(TOKENIZER_SRC)
        # "--" stops `go run` from treating a trailing .go-suffixed
        # argument as a second source file of the helper's own package.
        out = subprocess.run(
            ["go", "run", str(prog), "--", str(path)], capture_output=True, text=True
        )
        if out.returncode != 0:
            raise MutationError(f"tokenizer failed on {path}: {out.stderr}")
        # A file with no candidate tokens marshals as a nil Go slice,
        # which prints as the JSON literal null.
        return json.loads(out.stdout) or []


def sites_from_tokens(tokens: list[dict], data: bytes, path: Path) -> list[Site]:
    """sites_from_tokens turns raw scanner tokens into mutation sites.

    A `!` token immediately followed by a `=` token is never a
    candidate: turning `!=` into a bare `=` does not compile. This
    guard is defensive; go/scanner already merges `!=` into one NEQ
    token, so a real `!` token is never adjacent to `=` in practice.
    """
    sites = []
    for tok in tokens:
        kind = tok["tok"]
        start, end = tok["pos"], tok["end"]
        if kind in OPERATOR_MUTATIONS:
            sites.append(Site(path, start, end, kind, OPERATOR_MUTATIONS[kind], kind))
        elif kind == "!":
            if data[end : end + 1] == b"=":
                continue
            sites.append(Site(path, start, end, "!", "", "NOT"))
        elif kind == "continue":
            sites.append(Site(path, start, end, "continue", "", "CONTINUE"))
    return sites


def sites_for_file(path: Path) -> list[Site]:
    """sites_for_file tokenizes path with the go/scanner helper and
    returns its mutation candidates."""
    data = path.read_bytes()
    tokens = run_go_tokenizer(path)
    return sites_from_tokens(tokens, data, path)
