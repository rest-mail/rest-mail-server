"""Fail closed on a missing, malformed or below-95% go tool cover total.

Uses the same fixed 95..100 range as go-spf's RFC verification workflow.
Keep the gate local until that workflow supports cross-package instrumentation
and go-imap's execution-bounded fuzz runs.
"""

from decimal import Decimal
import re
import sys


def coverage_total(report):
    totals = [line for line in report.splitlines() if line.startswith('total:')]
    if len(totals) != 1:
        raise ValueError('expected exactly one coverage total')
    match = re.fullmatch(r'total:\s+\(statements\)\s+([0-9]+(?:\.[0-9]+)?)%\s*', totals[0])
    if not match:
        raise ValueError('malformed coverage total')
    total = Decimal(match[1])
    if not Decimal(95) <= total <= Decimal(100):
        raise ValueError(f'statement coverage {total}% is outside 95..100%')
    return total


if __name__ == '__main__':
    try:
        total = coverage_total(sys.stdin.read())
    except ValueError as error:
        sys.exit(str(error))
    print(f'Statement coverage: {total}%')
