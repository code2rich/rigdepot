---
name: unit-testing
description: Generate and run unit tests for a change, covering edge cases. Use when a change needs unit tests.
license: Apache-2.0
metadata:
  x-team: platform
---

# Unit testing

1. Find the classes the change touched.
2. Write tests for the boundaries listed in `references/boundaries.md`.
3. Run `scripts/run-tests.sh` and read the report in `$TEST_REPORT_DIR`.
