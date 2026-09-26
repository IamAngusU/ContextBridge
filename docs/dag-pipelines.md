# Bounded DAG pipeline contract

ContextBridge can validate an operator-defined dependency graph without
silently changing the semantics of existing pipelines. This is the first,
validation-only slice of DAG support.

## Current boundary

- Existing pipelines with no `mode` remain linear and execute exactly in
  declaration order.
- `mode: linear` may be written explicitly. Linear pipelines must not set
  `depends_on` or `max_parallel`.
- `mode: dag` is parsed and fully validated when configuration loads.
- A valid DAG is listed with `execution_supported: false`.
- Trying to run a DAG returns HTTP `409 Conflict` before a pipeline run or any
  child job is persisted.

The last two points are intentional. A parallel executor needs durable
per-node checkpoints and honest sibling/ambiguity semantics. Merely accepting
`depends_on` and then using the old linear executor would be an unsafe partial
implementation.

## Contract

```yaml
cluster:
  pipelines:
    compare:
      mode: dag
      max_parallel: 2
      steps:
        - name: extract_a
          depends_on: []
          input: '${input}'
          requirements:
            task: generation
        - name: extract_b
          depends_on: []
          input: '${input}'
          requirements:
            task: generation
        - name: synthesize
          depends_on: [extract_a, extract_b]
          input: '{"a":${steps.extract_a.output},"b":${steps.extract_b.output}}'
          requirements:
            task: generation
```

The graph is static operator configuration. Model output cannot create steps,
edges, requirements, routes or authority.

## Validation and limits

The complete graph is rejected before service startup when it contains:

- an unknown mode, dependency, duplicate dependency or self-dependency;
- a cycle;
- more than 16 requested parallel children;
- more than 32 incoming or outgoing edges for one step;
- more than 1,024 edges in total;
- `previous`, an implicit empty input, or an unsupported placeholder;
- a step-output reference that is not also a direct dependency;
- pipeline/step iteration or continue fields.

`${input}` and `${steps.NAME.output}` for direct dependencies are the only DAG
v1 placeholders. Named dependencies avoid inventing a meaning for `previous`
when several predecessors may complete in a different wall-clock order.

Topological planning is deterministic. When several nodes are ready together,
their stable order is their order in operator configuration. This order is
evidence and a reproducible admission tie-breaker; it does not bypass future
worker capacity, producer limits or execution policy.

The authenticated protocol manifest advertises
`dag_pipeline_contract_validation_v1`, `durable_dag_checkpoint_contract_v1`
and the exact fixed limits. It does not claim a DAG executor.

## Durable graph truth

The core also defines the checkpoint record that a future executor must use:

- the normalized operator configuration is bound by SHA-256;
- a second SHA-256 binds the immutable step/dependency graph and stable
  topological order;
- every node has one explicit state: `not_ready`, `ready`, `queued`, `running`,
  `completed`, `failed`, `cancelled`, `blocked_by_dependency`, or `ambiguous`;
- a persisted graph, dependency list, topological order and child job identity
  cannot be replaced later;
- terminal node states cannot move backwards;
- checkpoint timestamps cannot move backwards.

Store validation rejects a malformed binding or invalid transition before the
Bolt transaction changes durable state. This means later executor work can use
one persisted vocabulary rather than infer graph truth from log messages. The
checkpoint contract itself still does not dispatch a job.

The store also has one atomic admission primitive for future DAG execution. A
ready-node transition, governed child job, queue/index records, producer rate
consumption, authoritative job events and `pipeline.step.queued` event commit
in one Bolt transaction. An injected event failure rolls all of them back.
This removes the crash window where a child could exist while the graph still
claimed the node was merely ready. The public run endpoint remains disabled;
the primitive is a tested invariant, not a partially enabled executor.

## What remains before execution can be enabled

The next slices must add and prove:

1. bounded ready-set scheduling around the atomic admission primitive;
2. descendant blocking after failed, cancelled or ambiguous predecessors;
3. honest treatment of siblings that were already dispatched;
4. restart tests at every checkpoint, exact usage accounting, and authoritative
   graph events.

Until those properties exist, linear pipelines are the supported execution
path. This contract is useful for validating and reviewing future workflows,
not a promise that they already run in parallel.
