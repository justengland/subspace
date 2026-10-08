Workflow Object Model
Overview

The workflow system consists of two primary concerns:

Workflow Definition — describes what should be executed.
Workflow Runtime — managed by the Engine and describes what is currently or has been executed.

The core definition hierarchy is:

Workflow
└── Steps
    ├── Step
    │   └── Processes
    │       ├── Process
    │       └── Process
    └── Step
        └── Processes


The runtime hierarchy is:

Engine
└── WorkflowRuns
    └── WorkflowRun
        ├── StepRuns
        │   └── ProcessRuns
        └── ...

1. Workflow

A Workflow is the reusable definition of an entire workflow.

Workflow
- id
- name
- description
- version
- steps[]
- connections[]
- visualization?

Responsibilities
Define the workflow structure.
Own the collection of Steps.
Define connections between Steps.
Store workflow-level visualization data.
Provide a reusable definition that can have multiple WorkflowRun instances.

A Workflow does not contain runtime state.

2. Step

A Step represents a logical unit of execution within a Workflow.

Step
- id
- name
- description
- mode
- inputs[]
- outputs[]
- processes[]
- configuration
- visualization?


A Step can contain one or more Processes.

The Step's mode determines how those Processes are executed.

Step Modes

There are four supported modes:

PARALLEL
SERIES
DECISION
LOOP

2.1 Parallel Step

A parallel Step starts all of its Processes at approximately the same time.

             ┌── Process A ──┐
             │               │
Step ────────┼── Process B ──┼──► Step Complete
             │               │
             └── Process C ──┘


The Step completes when all Processes have completed.

Behavior
Start Step
    │
    ├── Start Process A
    ├── Start Process B
    └── Start Process C
             │
             ▼
     Wait for all processes
             │
             ▼
       Step completes

2.2 Series Step

A series Step executes its Processes sequentially.

Step
 │
 ▼
Process A
 │
 ▼
Process B
 │
 ▼
Process C
 │
 ▼
Step Complete


The next Process starts only after the previous Process has completed.

The Step completes after all Processes have completed.

2.3 Decision Step

A decision Step selects exactly one Process for execution based on its inputs.

                 ┌── Process A
                 │
Step ── Decision ┼── Process B
                 │
                 └── Process C


Only one Process is executed.

The decision logic should be based on the Step's inputs.

Example:

if input.type == "csv"
    execute Process A

if input.type == "json"
    execute Process B

if input.type == "xml"
    execute Process C


The decision configuration may contain:

DecisionConfiguration
- conditions[]


Where each condition identifies the Process that should execute when the condition evaluates to true.

2.4 Loop Step

A loop Step allows workflow execution to move repeatedly through the workflow.

A loop can move either:

Forward
Backward
Step A
   │
   ▼
Loop Step
   │
   ├── forward ──► Step B
   │
   └── backward ─► Step A

Loop Configuration
LoopConfiguration
- direction
- maxIterations


Where:

LoopDirection
- FORWARD
- BACKWARD


The default value for maxIterations is:

3


The value can be overridden for an individual Loop Step.

The iteration count should be tracked at runtime by StepRun.

3. Process

A Process represents the definition of a Unix process that the Engine can execute.

It is the lowest-level executable object in the workflow definition.

Process
- id
- name
- command
- arguments[]
- environment
- workingDirectory
- configuration


Example:

Process
- name: Compile
- command: gcc
- arguments:
    - main.c
    - -o
    - application
- workingDirectory: /workspace


A Process describes what should be executed.

It does not contain runtime information such as PID or exit code.

4. ProcessRun

A ProcessRun represents an actual Unix process created during a Workflow Run.

ProcessRun
- id
- processId
- pid
- status
- exitCode
- stdout
- stderr
- startedAt
- completedAt


The distinction is important:

Process
    │
    │ creates
    ▼
ProcessRun
    │
    │ executes
    ▼
Unix OS Process

Process vs ProcessRun
Process	ProcessRun
Definition	Runtime instance
Command	PID
Arguments	Status
Environment	Exit code
Working directory	stdout
Configuration	stderr
Reusable	One execution

A single Process can therefore have many ProcessRuns.

5. Inputs and Outputs

Steps communicate with each other through Inputs and Outputs.

Step A
  Output: data
       │
       ▼
Step B
  Input: data

Input
Input
- name
- type
- source
- value

Output
Output
- name
- type
- value


The source of an Input can reference an Output from another Step.

For example:

Step B
    inputs:
        sourceFile
            source:
                stepId: step-a
                output: compiledFile

6. Connections

Connections explicitly represent data flow between Steps.

Connection
- id
- sourceStepId
- sourceOutput
- targetStepId
- targetInput
- visualization?


Example:

Step A
  output: compiledBinary
        │
        │ Connection
        ▼
Step B
  input: binary


Connections allow the Engine and visualization layer to understand the workflow as a directed graph.

Step A ─────────► Step B ─────────► Step C

7. Engine

The Engine is responsible for executing and managing Workflows.

The Engine is separate from the Workflow definition.

Engine
- start(workflow)
- stop(run)
- pause(run)
- resume(run)
- debug(run)
- getRun(runId)
- listRuns()

Responsibilities
Start Workflow Runs.
Stop Workflow Runs.
Track active Workflow Runs.
Track completed Workflow Runs.
Orchestrate Step execution.
Create and manage ProcessRuns.
Handle Process lifecycle.
Handle parallel execution.
Handle series execution.
Evaluate decisions.
Manage loops.
Support debugging.
Capture runtime state.
Provide runtime information to the UI.

The Engine does not define the workflow itself.

8. WorkflowRun

A WorkflowRun represents one execution of a Workflow.

WorkflowRun
- id
- workflowId
- workflowVersion
- status
- inputs
- outputs
- startedAt
- completedAt
- stepRuns[]


A single Workflow can have many WorkflowRuns.

Workflow
   │
   ├── WorkflowRun #1001
   ├── WorkflowRun #1002
   └── WorkflowRun #1003


This allows multiple executions of the same workflow.

9. StepRun

A StepRun represents the runtime execution of a Step.

StepRun
- id
- stepId
- status
- inputs
- outputs
- startedAt
- completedAt
- processRuns[]
- iteration


The StepRun contains runtime state and references the Step definition.

Step
 │
 │ executes
 ▼
StepRun


For a Loop Step, iteration identifies the current loop iteration.

10. Runtime Hierarchy

The complete runtime hierarchy is:

Engine
└── WorkflowRun
    │
    ├── StepRun
    │   ├── ProcessRun
    │   └── ProcessRun
    │
    ├── StepRun
    │   └── ProcessRun
    │
    └── StepRun
        ├── ProcessRun
        └── ProcessRun


The relationship between definition and runtime is:

Workflow       ─────────► WorkflowRun
    │                           │
    │                           │
    ▼                           ▼
  Step        ─────────────► StepRun
    │                           │
    │                           │
    ▼                           ▼
 Process      ─────────────► ProcessRun

11. Visualization

Workflow objects can contain optional visualization data.

Visualization data represents how an object is displayed in the workflow UI.

It does not affect workflow execution.

Visualization
- position
- size
- metadata


Example:

visualization:
    position:
        x: 420
        y: 180

    size:
        width: 240
        height: 120


Visualization data may be stored on:

Workflow
Step
Process
Connection
12. Auto Layout

Visualization data is optional.

If an object does not have visualization data, the UI should automatically determine its layout.

if visualization exists
    use stored visualization
else
    auto-layout


This allows workflows to be created programmatically without requiring coordinates.

For example:

New Step
    │
    ├── No visualization data
    │
    ▼
Auto-layout engine
    │
    ▼
Position determined by UI


When a user manually drags an object, its visualization data can be persisted:

Step.visualization.position = {
    x: 500,
    y: 240
}


On subsequent loads, the stored position is used instead of automatically laying out that Step.

13. Visualization vs Execution

Visualization should remain separate from execution semantics.

Workflow
│
├── Definition
│   ├── Steps
│   ├── Processes
│   └── Connections
│
└── Visualization
    ├── Positions
    ├── Sizes
    └── UI metadata


The Engine should not depend on visualization coordinates.

Moving a Step in the UI must not change how the workflow executes.

14. Complete Object Model
                        ┌─────────────────┐
                        │    Workflow     │
                        │   Definition    │
                        └────────┬────────┘
                                 │
              ┌──────────────────┼──────────────────┐
              │                  │                  │
              ▼                  ▼                  ▼
           Steps            Connections       Visualization
              │
              ▼
            Step
              │
       ┌──────┼──────────────┐
       │      │              │
       ▼      ▼              ▼
    Inputs  Outputs       Processes
                              │
                              ▼
                           Process


                        ┌─────────────────┐
                        │      Engine     │
                        └────────┬────────┘
                                 │
                                 ▼
                        ┌─────────────────┐
                        │  WorkflowRun    │
                        └────────┬────────┘
                                 │
                                 ▼
                            StepRuns
                                 │
                                 ▼
                           ProcessRuns
                                 │
                                 ▼
                         Unix OS Process

15. Design Principles
Definition vs Runtime

Definitions describe what should happen.

Runtime objects describe what did happen or is happening.

Definition                  Runtime
────────────────────────────────────────
Workflow              →     WorkflowRun
Step                  →     StepRun
Process               →     ProcessRun

Step vs Process

A Step represents execution semantics.

A Process represents the Unix command to execute.

Step
  mode = PARALLEL

  ├── Process A
  ├── Process B
  └── Process C


The Step determines how those Processes are orchestrated.

Visualization Is Metadata

Visualization data controls presentation, not execution.

A workflow must remain executable even when no visualization data exists.

Connections Define Data Flow

Inputs and Outputs define what data a Step consumes and produces.

Connections define how those values flow between Steps.

Engine Owns Runtime State

The Engine is responsible for managing WorkflowRuns and their associated StepRuns and ProcessRuns.

The Workflow definition itself should remain reusable and independent of any particular execution.

16. Summary

The resulting architecture is:

                    WORKFLOW DEFINITION
                           │
                           ▼
                       Workflow
                           │
              ┌────────────┼────────────┐
              │            │            │
              ▼            ▼            ▼
            Steps     Connections   Visualization
              │
              ▼
            Step
              │
      ┌───────┼────────┐
      │       │        │
   Inputs  Outputs  Processes
                        │
                        ▼
                     Process


                    RUNTIME / ENGINE
                           │
                           ▼
                        Engine
                           │
                           ▼
                     WorkflowRun
                           │
                           ▼
                        StepRun
                           │
                           ▼
                      ProcessRun
                           │
                           ▼
                    Unix OS Process


The core model is therefore:

Workflow → Step → Process

with:

Inputs/Outputs + Connections defining data flow.
Step modes defining execution semantics.
Visualization defining UI presentation.
Engine → WorkflowRun → StepRun → ProcessRun defining runtime execution.
Auto-layout providing positions when visualization data has not been persisted.
