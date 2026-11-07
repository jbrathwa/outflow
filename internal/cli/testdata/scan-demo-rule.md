# outflow report

Cluster **aks-prod-eus** · rule: allow US · window 2026-11-06T09:00:00Z → 2026-11-07T09:00:00Z (UTC, RFC3339)

Coverage: FULL

| Status | Workload | Destination | Location | Evidence | Action |
|---|---|---|---|---|---|
| outside | payments/ledger-worker | stledgerweu.blob.core.windows.net (20.60.40.4):443 | NL (azure:westeurope) | confirmed | allow |
| unknown | payments/ledger-worker | login.microsoftonline.com (20.190.151.68):443 | global | confirmed | allow |
| outside | analytics/event-collector | api.eu.segment.io (52.209.81.3):443 | IE (aws:eu-west-1) | confirmed | allow |
| outside | (node) aks-np1-vmss0003 | 51.15.22.140:123 | FR | node-level | allow |
| unknown | ml/model-fetch | huggingface.co (13.33.30.71):443 | global | confirmed | allow |
| unknown | ml/model-fetch | storage.googleapis.com (142.250.80.27):443 | global | confirmed | allow |
| unknown | ml/model-fetch | models.fetch-hub.example (52.210.17.44) | IE (aws:eu-west-1) | DNS only | — |
