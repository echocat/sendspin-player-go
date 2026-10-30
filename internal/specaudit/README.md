# Specification audit tooling

This package maintains the pinned RC1 requirement catalog and its test mappings.
It is internal development tooling, not part of the public SDK API.

- [Requirement catalog](../../docs/rc1-requirements.json)
- [Source index](../../docs/rc1-source-index.json)
- [Supplemental inventory](../../docs/rc1-template-inventory.json)
- [Generated audit](../../docs/rc1-audit.md), rendered from its [template](../../docs/rc1-audit.template.md)
- Review records: [interpretation decisions](reviews/rc1-review-decisions.md) and
  [cryptographic source review](reviews/cryptographic-review.md)

`mise format:audit` regenerates the audit; `mise lint:audit` checks consistency.
`mise audit:report` evaluates explicitly recorded execution evidence. Missing,
stale, skipped, or failed evidence is not a pass. Test results do not change the
catalog's reviewed assessments.
