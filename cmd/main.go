package main

import (
	"context"
	"gpu-metric-collector/pkg/collector"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"k8s.io/client-go/informers"
)

func main() {
	quitChan := make(chan struct{})
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	metricCollector := collector.NewMetricCollector()

	informerFactory := informers.NewSharedInformerFactory(metricCollector.HostKubeClient, 0)
	collector.AddAllEventHandlers(metricCollector, informerFactory)

	metricCollector.MultiMetric.DumpMultiMetric()

	wg.Add(1)
	go informerFactory.Start(quitChan)

	wg.Add(1)
	metricCollector.RunMetricCollector(ctx)

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case <-signalChan:
			close(quitChan)
			cancel()
			wg.Wait()
			os.Exit(0)
		}
	}
}
