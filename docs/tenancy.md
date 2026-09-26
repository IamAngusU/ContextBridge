# Producer identity and tenant labels

ContextBridge authenticates a producer credential. A request's `tenant_id` is
a logical namespace and execution-policy selector; by itself it is not an
authenticated customer identity.

This distinction matters when several applications or customers share one
relay:

```text
producer token subject   authenticated application identity
tenant_id                logical namespace selected by the request
allowed_tenants          optional token-bound tenant_id authority
```

An application must still authenticate and authorize its own end users. Job
ownership is bound to the producer subject. RAG storage additionally derives
its namespace from the authenticated producer plus the logical tenant, so two
producer credentials do not gain shared RAG access merely by choosing the same
tenant label.

## Bind a credential to one tenant

For a customer- or project-specific credential, bind the allowed label when
the administrator issues it:

```sh
contextbridge cluster token create \
  --role producer \
  --subject billing-service \
  --allowed-tenants customer-42
```

The relay stores this scope with the credential. If a request omits
`tenant_id`, the single allowed value is applied before execution-policy
selection. A different value is rejected with the stable
`scope.tenant_forbidden` admission code.

The safer server-integration command keeps the bearer token out of terminal
output:

```sh
contextbridge integrate relay \
  --subject billing-service \
  --allowed-tenants customer-42 \
  --write-env ./billing-contextbridge.env
```

## Allow a bounded set

One credential may be allowed to choose among a small explicit set:

```sh
contextbridge cluster token create \
  --role producer \
  --subject regional-router \
  --allowed-tenants eu-a,eu-b
```

With more than one allowed value, the request must provide an exact
`tenant_id`. Tenant policy keys are case-sensitive selectors; scopes reject
case-insensitively ambiguous duplicates and do not silently change casing.

Enforcement occurs before execution-policy lookup on normal submission,
contract validation, route explanation, encrypted assignment reservation and
pipeline admission. Durable job admission checks the bound scope again. An
idempotency retry cannot change the tenant context of its original request.

## Backwards compatibility and limits

A producer credential without `allowed_tenants` retains the existing behavior:
the caller may select a bounded `tenant_id`, subject to relay execution policy.
That mode is useful for a trusted first-party producer, but operators must not
describe the caller-selected label as customer authentication.

Tenant scopes strengthen one authorization boundary; they do not make the
current relay a production-ready hosted multi-tenant control plane. They do
not hide tenant, ownership, routing, timing or usage metadata from the relay,
and they do not replace application login, per-user authorization, rate/cost
governance, TLS, E2EE, audit retention or operational isolation.
