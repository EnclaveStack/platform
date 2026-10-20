---
description: Run the governance gate and report what is out of compliance
---
Run `bash bin/statutory-integrity.sh`. If it fails, fix each failure at its owning layer (register missing docs, correct stale registry rows, repair headers) and re-run until it passes. Then list `git status` so the user can review. Do not commit.
