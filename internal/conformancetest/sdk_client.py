"""Harness launch shim only: protocol handling stays in the Go SDK adapter."""
import os
import sys

if __name__ == "__main__":
    binary = os.environ["SENDSPIN_CONFORMANCE_CLIENT"]
    os.execv(binary, [binary, *sys.argv[1:]])
