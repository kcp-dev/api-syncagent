# Troubleshooting

## An `APIBinding` reports `Invalid APIExport: APIResourceSchema ... not found`

A consumer sees this condition on the `APIBinding` that binds the APIExport:

```
APIExportValid=False   InternalError
  Invalid APIExport. Please contact the APIExport owner to resolve:
  APIResourceSchema "v4ed05578.widgets.example.com" not found
```

and the API group stops being served in the bound workspace (`error: the server doesn't have a
resource type "widgets"`), while the `APIExport` that is referenced looks completely healthy:
its status carries only the usual conditions (`IdentityValid`) and the Sync Agent that manages it
logs nothing about the broken entry. `mergeResourceSchemas()` in the agent keeps every entry in
`spec.latestResourceSchemas` / `spec.resources[].schema` that belongs to a resource no
`PublishedResource` manages — intentionally, so that removing a `PublishedResource` does not
destroy user data — but it also never checks that the referenced schema actually exists.

### Finding the entry

Compare the schema names in the export against the schemas in the same workspace:

```sh
$ kubectl get apibinding <binding> \
    -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason} {.message}{"\n"}{end}'

$ kubectl get apiexport -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.resources[*].schema}{"\n"}{end}'
# older kcp versions keep the same list in .spec.latestResourceSchemas

$ kubectl get apiresourceschemas
```

The name from the `APIBinding` condition that does not appear in the `APIResourceSchema` list is
the dangling entry.

`kubectl get events` in the workspace where the export lives additionally shows what the Sync
Agent does to the export (`AddingResourceSchemas` / `RemovingResourceSchemas`), which helps to
tell an agent-managed entry from a hand-written one.

### Recovering

**If the resource should exist**, fix the reason the schema is missing instead: the Sync Agent
derives it from the `PublishedResource`'s projected CRD, so look at

```sh
$ kubectl get publishedresource <name> -o jsonpath='{.status.resourceSchemaName}'   # service cluster
$ kubectl -n <agent-namespace> logs deploy/<agent> | grep -i apiresourceschema
```

A `PublishedResource` whose CRD cannot be resolved (CRD not installed yet, projection rules
matching nothing) never sets that status, and the agent then leaves the whole export alone,
broken entries included.

**If the resource is no longer needed**, remove the entry from the `APIExport`. This is a
metadata-only change: it stops the API from being served, it does not delete the
`APIResourceSchema` and it does not touch synced objects, neither on the service cluster nor in
the workspaces of the consumers. Objects that already exist stay where they are.

```sh
# v1alpha2 exports: drop the entry by list index
$ kubectl get apiexport <name> -o jsonpath='{.spec.resources}'             # find the index
$ kubectl patch apiexport <name> --type=json -p '[{"op":"remove","path":"/spec/resources/<i>"}]'

# older kcp versions
$ kubectl patch apiexport <name> --type=json -p '[{"op":"remove","path":"/spec/latestResourceSchemas/<i>"}]'
```

After that the consumers' `APIBinding`s go back to `APIExportValid=True` on their next
reconciliation.

Note that this does not stick for a resource that a Sync Agent still manages: the next reconcile
of that `PublishedResource` adds the agent's own schema name back and drops the entry that was
manually added for the same group and resource (`Warning RemovingResourceSchemas` event on the
`APIExport`). For those resources the underlying cause has to be fixed instead.
