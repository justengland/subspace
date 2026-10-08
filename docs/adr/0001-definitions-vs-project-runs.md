# Definitions in Subspace, runs against a Project

**Superseded by ADR-0003** (`0003-repo-scoped-workflows-under-home.md`).

~~Subspace stores Workflow YAML in this monorepo (`workflows/`), but a WorkflowRun executes Unix Processes against a Project directory that is often another checkout. Run artifacts live under `~/.local/subspace/<project-basename>/<run-id>/` with time-sortable short IDs, not inside either git tree. That keeps definitions versioned with the tool, execution rooted where the work is, and run history off the Project's dirty tree.~~
