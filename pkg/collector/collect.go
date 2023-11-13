package collector

import (
	"context"
	"gpu-metric-collector/pkg/api/metric"
	"log"
	"net"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"google.golang.org/grpc"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

func (m *MetricCollector) RunMetricCollector(ctx context.Context) {
	go m.StartGRPCServer(ctx)
	go wait.UntilWithContext(ctx, m.MetricCollectingCycle, *m.Interval)
}

func (m *MetricCollector) MetricCollectingCycle(ctx context.Context) {
	m.SafeMultiMetric.mutex.Lock()
	defer m.SafeMultiMetric.mutex.Unlock()

	KETI_LOG_L1("[cycle] metric collect cycle run")

	node, err := m.HostKubeClient.CoreV1().Nodes().Get(context.TODO(), m.SafeMultiMetric.MultiMetric.NodeName, v1.GetOptions{})
	if err != nil {
		log.Fatalf("cannot get node metric: %s", err)
	}

	m.SafeMultiMetric.MultiMetric.NodeMetric.MemoryFree, _ = node.Status.Allocatable.Memory().AsInt64()
	m.SafeMultiMetric.MultiMetric.NodeMetric.MilliCpuFree = node.Status.Allocatable.Cpu().MilliValue()
	m.SafeMultiMetric.MultiMetric.NodeMetric.StorageFree, _ = node.Status.Allocatable.Storage().AsInt64()
	m.SafeMultiMetric.MultiMetric.NodeMetric.NetworkRx = 0 //??
	m.SafeMultiMetric.MultiMetric.NodeMetric.NetworkTx = 0 //??

	if m.SafeMultiMetric.MultiMetric.GpuCount != 0 {
		defer func() {
			ret := nvml.Shutdown()
			if ret != nvml.SUCCESS {
				log.Fatalf("Unable to shutdown NVML: %v", ret)
			}
		}()

		for uuid, gpuMetric := range m.SafeMultiMetric.MultiMetric.GpuMetrics {
			ret := nvml.Init()
			if ret != nvml.SUCCESS {
				log.Fatalf("Unable nvml.Init(): %v", ret)
			}

			device, ret := nvml.DeviceGetHandleByUUID(uuid)
			if ret != nvml.SUCCESS {
				log.Fatalf("Unable to get device at uuid %s: %v", uuid, ret)
			}

			memory, _ := device.GetMemoryInfo()
			gpuMetric.MemoryUsed = int64(memory.Used)

			power, _ := device.GetPowerUsage()
			gpuMetric.PowerUsed = int64(power)

			picTX, _ := device.GetPcieThroughput(0)
			gpuMetric.PciTx = int64(picTX)

			pciRX, _ := device.GetPcieThroughput(1)
			gpuMetric.PciRx = int64(pciRX)

			temperature, _ := device.GetTemperature(0)
			gpuMetric.Temperature = int64(temperature)

			utilization, _ := device.GetUtilizationRates()
			gpuMetric.Utilization = int64(utilization.Gpu)

			fanSpeed, _ := device.GetFanSpeed()
			gpuMetric.FanSpeed = int64(fanSpeed)

			gpuMetric.PodCount = 0
			//runningPods +++
		}
	}

	DumpMultiMetric(m.SafeMultiMetric.MultiMetric)        // DEBUGG LEVEL = 1 일때 출력 수행
	DumpMultiMetricForTest(m.SafeMultiMetric.MultiMetric) // DEBUGG LEVEL = 3 일때 (정량) 출력 수행
}

func (m *MetricCollector) GetMultiMetric(context.Context, *metric.Request) (*metric.MultiMetric, error) {
	m.SafeMultiMetric.mutex.Lock()
	defer m.SafeMultiMetric.mutex.Unlock()

	KETI_LOG_L1("[gRPC] get multi metric called")

	return m.SafeMultiMetric.MultiMetric, nil
}

func (m *MetricCollector) StartGRPCServer(ctx context.Context) {
	lis, err := net.Listen("tcp", ":9323")
	if err != nil {
		klog.Fatalf("failed to listen: %v", err)
	}
	metricServer := grpc.NewServer()
	metric.RegisterMetricCollectorServer(metricServer, m)
	KETI_LOG_L1("[gRPC] metric collector server running...")
	if err := metricServer.Serve(lis); err != nil {
		klog.Fatalf("failed to serve: %v", err)
	}
}
