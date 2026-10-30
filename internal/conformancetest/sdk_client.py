"""Harness launch shim only: protocol handling stays in the Go SDK adapter."""
import os
import subprocess
import sys

if __name__ == "__main__":
    binary = os.environ["SENDSPIN_CONFORMANCE_CLIENT"]
    argv = [binary, *sys.argv[1:]]
    if os.name == "nt":
        # CPython's _wexecv delegates to the CRT, which joins argv with spaces
        # without quoting (ucrt/exec/cenvarg.cpp: construct_command_line).
        # Quote each argument for the receiving Go/Windows argv parser, not a shell.
        argv = [subprocess.list2cmdline([arg]) for arg in argv]
    os.execv(binary, argv)
