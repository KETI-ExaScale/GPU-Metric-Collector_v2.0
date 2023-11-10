#!/bin/bash

target="keti-gpu-metric-collector"
namespace="gpu"

pod_list=$(kubectl get pods -o=name -n $namespace | sed "s/^.\{4\}//")
for pod_name in $pod_list; do
  echo "------------------------------------"
  if [[ "$pod_name" == *$target* ]]; then
    kubectl logs --tail=32 $pod_name -n $namespace
    echo "------------------------------------"
  fi
done
