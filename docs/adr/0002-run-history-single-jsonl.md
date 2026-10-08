# Run history is one append-only JSONL

Each WorkflowRun directory holds a single append-only JSONL stream for state transitions and Process stdout/stderr chunks. Separate log files and snapshot rewrites were rejected so the debugger can tail one timeline and replay events over WebSocket without a second storage shape. The trade-off is larger run files when commands are noisy; split logs later if that hurts.
