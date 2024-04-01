#!/bin/bash

target="gpu-metric-collector"
namespace="gpu"

while [ -z $PODNAME ]
do
    PODNAME=`kubectl get pods -n $namespace -o jsonpath="{.items[0].metadata.name}" --field-selector spec.nodeName="$1" --selector=name=$target` 
done

kubectl logs $PODNAME -n $namespace -f --tail=100