# AgentSuite policy contract (v1alpha1)

`kmx suite render-policy` validates a portable `AgentPolicy` and `AgentPolicyBinding` and renders namespaced Orka resources without contacting a cluster:

```console
kmx suite render-policy policy.json binding.json --namespace agents > policy.yaml
```

The command rejects unknown JSON fields, unsupported capabilities, empty or oversized policies, duplicate rules, unclean or non-absolute object paths, invalid validity windows, and a binding whose source UID or digest does not match the policy. Digests use RFC 8785 JCS over sorted semantic arrays and a domain/version prefix.

The supported authoring control is deliberately narrow: exact-path write denial for the configured enforcement runtime. Network policy and network-quarantine actions are rejected before rendering. This version does not claim coverage for read confinement, create, replace, rename, unlink, mmap, inherited writable descriptors, `io_uring`, hard links, or process termination. Orka and its configured enforcement runtime must accept the binding before Task creation or dispatch; rendering alone is not enforcement.
