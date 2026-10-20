---
description: Create a new governed document in the right layer, registered correctly
---
Create a governed doc: $ARGUMENTS

1. Pick the layer that owns the subject (see `doc-governance`). 2. Find the next unused number in that layer (never reuse). 3. Create `[LL]-[NNN]-[slug]-v1.md` from `docs/L0-foundation/L0-001-master-document-template-v1.md`. 4. Register it in the layer's `-000-artifact-registry-v1.md`. 5. Add a CHANGELOG entry. 6. Run `bash bin/statutory-integrity.sh` and show the result.
