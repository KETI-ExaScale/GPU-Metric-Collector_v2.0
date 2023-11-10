package collector

import (
	"fmt"
	"strconv"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

const configMapName = "gpu-metric-collector-configmap"

const (
	Policy1 = "metric-collecting-cycle"
)

func AddAllEventHandlers(metricCollector *MetricCollector, informerFactory informers.SharedInformerFactory) {
	informerFactory.Core().V1().ConfigMaps().Informer().AddEventHandler(
		cache.FilteringResourceEventHandler{
			FilterFunc: func(obj interface{}) bool {
				switch t := obj.(type) {
				case *v1.ConfigMap:
					return (t.ObjectMeta.Name == configMapName)
				case cache.DeletedFinalStateUnknown:
					return false
				default:
					KETI_LOG_L3("<error> configmap error\n")
					return false
				}
			},
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc:    metricCollector.SetPolicy,
				UpdateFunc: metricCollector.UpdatePolicy,
			},
		},
	)
}

func (m *MetricCollector) SetPolicy(obj interface{}) {
	configMap := obj.(*v1.ConfigMap)

	collectingCycle, _ := strconv.ParseInt(configMap.Data["metric-collecting-cycle"], 0, 32)

	m.CollectCycle = int32(collectingCycle)

	KETI_LOG_L2("\n-----:: GPU Metric Collector Policy List ::-----")
	KETI_LOG_L2(fmt.Sprintf("[policy 1] %s : %d", Policy1, collectingCycle))
}

func (m *MetricCollector) UpdatePolicy(oldObj, newObj interface{}) {
	configMap := newObj.(*v1.ConfigMap)

	collectingCycle, _ := strconv.ParseInt(configMap.Data["metric-collecting-cycle"], 0, 32)

	m.CollectCycle = int32(collectingCycle)

	KETI_LOG_L2("\n-----:: Updated GPU Metric Collector Policy List ::-----")
	KETI_LOG_L2(fmt.Sprintf("[policy 1] %s : %d", Policy1, collectingCycle))

	//updateCollectingCycle()
}
