# Workflow

Local workflow definitions and their executions. Definitions live with the Subspace tool; a WorkflowRun executes Processes against a chosen Project directory (often another repo).

## Definition

**Workflow**:
A reusable definition of an entire workflow: Steps, Connections, optional visualization, and an optional default Project. Holds no runtime state.
_Avoid_: Pipeline, job definition, playbook

**Step**:
A logical unit of execution inside a Workflow. Owns Inputs, Outputs, Processes, and a mode that says how those Processes run.
_Avoid_: Stage, task, node (prefer Step in prose; node is fine only for graph/UI talk)

**Process**:
The definition of a Unix command the Engine can run (command, arguments, environment, working directory). Holds no PID or exit code.
_Avoid_: Task, job, action, script (unless the command literally is a script file)

**Connection**:
A derived edge from one Step's Output to another Step's Input, computed from Input sources for the UI graph. Not stored as the source of truth.
_Avoid_: Link, edge (edge is fine only in graph/UI talk), binding

**Input**:
A named value a Step consumes. May take a literal value or reference another Step's Output. Input sources are the canonical data-flow wiring.
_Avoid_: Parameter, argument (reserve argument for Process CLI args)

**Output**:
A named value a Step produces for downstream Steps.
_Avoid_: Result, artifact (artifact means a file on disk produced by a run)

**Visualization**:
Optional layout metadata (position, size, UI metadata) on Workflow, Step, Process, or Connection. Never affects execution.

**Step mode**:
How a Step runs its Processes, then (for some modes) where the WorkflowRun goes next: PARALLEL, SERIES, DECISION, or LOOP.

**Decision**:
A Step mode that selects exactly one of its Processes from ordered conditions over the Step's Inputs. Zero matches or more than one match fails the StepRun.
_Avoid_: Branch, switch, if-step

**Loop**:
A Step mode that runs the Step's Processes, then evaluates a condition and continues at either the previous Step or the next Step in the Workflow. A per-Step `maxIterations` cap (default 3) fails the StepRun when exceeded. Not a jump to an arbitrary Step, and not a repeat-until over the same Step's Processes alone.
_Avoid_: While, foreach, retry (retry is a different concern)

**Parallel**:
A Step mode that starts all of its Processes together and completes when all of them have completed successfully. A non-zero exit fails the StepRun and Stops sibling ProcessRuns.

**Series**:
A Step mode that runs its Processes one after another; the next starts only after the previous completes successfully.


## Runtime

**Project**:
The directory tree where a WorkflowRun executes Processes (their working tree / default cwd). Often a different repo than the one that stores Workflow definitions.
_Avoid_: Repo (unless you mean a git repository specifically), workspace, target, cwd (cwd is the Process field; Project is the run's root)

**Engine**:
The component that starts and manages WorkflowRuns: orchestration, Process lifecycle, and run state. Separate from any Workflow definition.

**WorkflowRun**:
One execution of a Workflow against a Project, including status, timing, and its StepRuns. The debugger drives a WorkflowRun (start, stop, pause, resume, rewind) rather than a separate session type.
_Avoid_: Execution, job, instance, debug session (say WorkflowRun)

**StepRun**:
The runtime execution of one Step inside a WorkflowRun, including status, I/O, ProcessRuns, and loop iteration when relevant.
_Avoid_: Step instance

**ProcessRun**:
One actual OS process spawned from a Process during a run: PID, status, exit code, stdout/stderr, timestamps.
_Avoid_: Process instance, invocation

**Pause**:
Ask the WorkflowRun to hold: wait until in-flight ProcessRuns exit on their own, then do not start the next Step.
_Avoid_: Suspend (OS-level), SIGSTOP, Stop

**Stop**:
End a WorkflowRun by sending SIGTERM to in-flight OS processes and refusing to schedule further Steps.
_Avoid_: Pause, kill (SIGKILL is a last resort, not the normal Stop)

**Rewind**:
Move a WorkflowRun back to an earlier Step and discard later StepRuns so resume re-executes from that Step.
_Avoid_: Undo, reset (reset implies the whole run), restart
