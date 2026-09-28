---
name: existing
description: "An existing flat `*.md` skill under $DSH_HOME/skills that Fleet must not touch."
---

Body of an existing user skill. `$DSH_HOME/skills/` is a **flat** `*.md` directory
(not the batch-one `<name>/SKILL.md` shape), so Fleet's own entries are written as
`<name>.md` symlinks and this real file stays untouched unless the profile names it.
