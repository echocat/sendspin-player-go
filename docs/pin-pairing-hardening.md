# Internal PIN failure-path review

This is a bounded internal code/test review, **not an independent cryptographic
audit, physical UI acceptance or expansion of the frozen primary conformance
assessments**. No device, capture, audio signal or global setting is involved.

## Reproduced defects and fixes

| Finding | Previous behavior | Fix / evidence |
| --- | --- | --- |
| Round-counter Store transactions lacked the attempt deadline | A Store honoring its supplied session context could block beyond the two-minute attempt; a late return could reach PIN display. Verified-confirmation counter resets used the same unbounded transaction path. | Shared counter transaction guard uses the attempt/earlier parent deadline, checks after acquiring the credential lock and after Save, and distinguishes a Store's own timeout from actual attempt expiry. Tests include a cooperative Store and one returning success after ignoring cancellation. |
| Peer cancellation could close an unrelated local static window | Session cleanup reset the Client's window without checking its owner. A pending/unrelated peer could cancel an unbound gesture or another connection's bound window. | Session cancellation closes only a window owned by that session. The deliberate local CancelPairing action retains its device-wide effect. |
| Pairing transport writes used only the general write timeout | A write started shortly before expiry could retain a longer per-message context. | Pair-init/auth/confirm/finalize/retry use the earlier attempt/parent/normal write deadline. Pair-pending and timeout abort remain outside an expired attempt budget. This is not distributed rollback of already-transmitted bytes. |

The deadline/window regressions were first run against the prior implementation
and failed, then passed with the fixes. Local receipts are retained under
`var/pin-pairing/hardening/`.

## Additional checks

- Independent reference-server scenarios refuse counter persistence before PIN
  emission and after successful server confirmation. Neither branch sends client
  confirmation/wrapped credentials or creates trust at either peer. An abandoned
  dynamic display is hidden exactly once.
- Gesture-gated static cancellation sends no pair-init or finalize and persists
  no pairing record.
- Fixed draft/Python confirmation vectors cannot authenticate modified password,
  context, session ID, associated roles or peer share. Failed confirmation clears
  live ISK/MAC state, prevents export and cannot be reused.
- Earlier parent deadlines and Store-originated timeout errors are not mistaken
  for an expired pairing attempt.

Relevant tests: `session_pairing_hardening_test.go`,
`client_pairing_hardening_e2e_test.go`, `internal/cpace/binding_test.go`.
The reference remains the locked, unmodified aiosendspin/cpace implementation;
only fixture operator input and controlled Store refusal are ours.

## Still separate

Arbitrarily blocking/context-ignoring callbacks or Stores cannot be forcibly
preempted. Application Store isolation, local gesture authorization, actual
display replacement/hiding, downgrade handling of credential files, physical
timing and independent cryptographic review remain product/review duties.
The optional PIN profile's source-inventory acceptance remains separate from
the default PSK-only audit. Test success does not certify these open duties.
