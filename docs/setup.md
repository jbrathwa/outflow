# Setting up outflow's data sources

`outflow doctor` lists what outflow needs; this guide covers how to turn each piece on.

## CoreDNS

On AKS, CoreDNS query logging is off by default. Without it outflow can see what left
through the firewall per node, but not which workload made each call. Enable it with the
`coredns-custom` ConfigMap in `kube-system`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns-custom
  namespace: kube-system
data:
  log.override: |
    log
```

Apply it and restart CoreDNS:

```sh
kubectl apply -f coredns-custom.yaml
kubectl -n kube-system rollout restart deployment coredns
```

Container Insights must also collect `kube-system` container logs, so the query lines
reach `ContainerLogV2` where outflow reads them.

## Coming next

Sections for Azure Firewall diagnostic settings and the roles outflow needs will be added
with live collection.
