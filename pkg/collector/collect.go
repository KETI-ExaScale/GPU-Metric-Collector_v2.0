package collector

import (
	"context"
	"fmt"
	"gpu-metric-collector/pkg/api/metric"
	"log"
	"net"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"google.golang.org/grpc"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

func (m *MetricCollector) RunMetricCollector(ctx context.Context) {
	go m.StartGRPCServer(ctx)
	go wait.UntilWithContext(ctx, m.MetricCollectingCycle, time.Duration(m.CollectCycle)*time.Second)
}

func (m *MetricCollector) MetricCollectingCycle(ctx context.Context) {
	m.MultiMetric.mutex.Lock()
	defer m.MultiMetric.mutex.Unlock()

	node, err := m.HostKubeClient.CoreV1().Nodes().Get(context.TODO(), m.MultiMetric.nodeName, v1.GetOptions{})
	if err != nil {
		log.Fatalf("cannot get node metric: %s", err)
	}

	m.MultiMetric.nodeMetric.memoryFree, _ = node.Status.Allocatable.Memory().AsInt64()
	m.MultiMetric.nodeMetric.milliCPUFree = node.Status.Allocatable.Cpu().MilliValue()
	m.MultiMetric.nodeMetric.storageFree, _ = node.Status.Allocatable.Storage().AsInt64()
	m.MultiMetric.nodeMetric.networkRX = 0 //??
	m.MultiMetric.nodeMetric.networkTX = 0 //??

	if m.MultiMetric.gpuCount != 0 {
		defer func() {
			ret := nvml.Shutdown()
			if ret != nvml.SUCCESS {
				//log.Fatalf("Unable to shutdown NVML: %v", ret)
				fmt.Printf("Unable to shutdown NVML: %v\n", ret)
			}
		}()

		for uuid, gpuMetric := range m.MultiMetric.gpuMetric {
			ret := nvml.Init()
			if ret != nvml.SUCCESS {
				log.Fatalf("Unable nvml.Init(): %v", ret)
			}

			device, ret := nvml.DeviceGetHandleByUUID(uuid)
			if ret != nvml.SUCCESS {
				log.Fatalf("Unable to get device at uuid %s: %v", uuid, ret)
			}

			memory, _ := device.GetMemoryInfo()
			gpuMetric.memoryUsed = int64(memory.Used)

			power, _ := device.GetPowerUsage()
			gpuMetric.powerUsed = int64(power)

			picTX, _ := device.GetPcieThroughput(0)
			gpuMetric.pciTX = int64(picTX)

			pciRX, _ := device.GetPcieThroughput(1)
			gpuMetric.pciRX = int64(pciRX)

			gpuMetric.memoryGauge = 0   //??
			gpuMetric.memoryCounter = 0 //??

			temperature, _ := device.GetTemperature(0)
			gpuMetric.temperature = int64(temperature)

			utilization, _ := device.GetUtilizationRates()
			gpuMetric.utilization = int64(utilization.Gpu)

			fanSpeed, _ := device.GetFanSpeed()
			gpuMetric.fanSpeed = int64(fanSpeed)

			gpuMetric.podCount = 0
			//runningPods +++
		}
	}

	m.MultiMetric.DumpMultiMetric() //출력 테스트
}

func (m *MetricCollector) GetMultiMetric(context.Context, *metric.Request) (*metric.MultiMetric, error) {
	m.MultiMetric.mutex.Lock()
	defer m.MultiMetric.mutex.Unlock()

	response := &metric.MultiMetric{}
	response.NodeName = m.MultiMetric.nodeName

	response.NodeMetric = &metric.NodeMetric{}
	response.NodeMetric.MilliCpuTotal = m.MultiMetric.nodeMetric.milliCPUTotal
	response.NodeMetric.MilliCpuFree = m.MultiMetric.nodeMetric.milliCPUFree
	response.NodeMetric.MemoryTotal = m.MultiMetric.nodeMetric.memoryTotal
	response.NodeMetric.MemoryFree = m.MultiMetric.nodeMetric.memoryFree
	response.NodeMetric.StorageTotal = m.MultiMetric.nodeMetric.storageTotal
	response.NodeMetric.StorageFree = m.MultiMetric.nodeMetric.storageFree
	response.NodeMetric.NetworkRx = m.MultiMetric.nodeMetric.networkRX
	response.NodeMetric.NetworkTx = m.MultiMetric.nodeMetric.networkTX
	response.GpuMetrics = make(map[string]*metric.GPUMetric)

	for uuid, gpuMetric := range m.MultiMetric.gpuMetric {
		response.GpuMetrics[uuid] = &metric.GPUMetric{}
		response.GpuMetrics[uuid].GpuName = gpuMetric.gpuName
		response.GpuMetrics[uuid].Architecture = gpuMetric.architecture
		response.GpuMetrics[uuid].MaxClock = gpuMetric.maxClock
		response.GpuMetrics[uuid].Cudacore = gpuMetric.cudacore
		response.GpuMetrics[uuid].Bandwidth = gpuMetric.bandwidth
		response.GpuMetrics[uuid].MaxOperativeTemp = gpuMetric.maxOperativeTemp
		response.GpuMetrics[uuid].SlowdownTemp = gpuMetric.slowdownTemp
		response.GpuMetrics[uuid].ShutdownTemp = gpuMetric.shutdownTemp
		response.GpuMetrics[uuid].MemoryTotal = gpuMetric.memoryTotal
		response.GpuMetrics[uuid].MemoryUsed = gpuMetric.memoryUsed
		response.GpuMetrics[uuid].PowerUsed = gpuMetric.powerUsed
		response.GpuMetrics[uuid].PciRx = gpuMetric.pciRX
		response.GpuMetrics[uuid].PciTx = gpuMetric.pciTX
		response.GpuMetrics[uuid].MemoryGauge = gpuMetric.memoryGauge
		response.GpuMetrics[uuid].MemoryCounter = gpuMetric.memoryCounter
		response.GpuMetrics[uuid].Temperature = gpuMetric.temperature
		response.GpuMetrics[uuid].Utilization = gpuMetric.utilization
		response.GpuMetrics[uuid].FanSpeed = gpuMetric.fanSpeed
		response.GpuMetrics[uuid].PodMetrics = make(map[string]*metric.PodMetric)

		for podName, podMetric := range gpuMetric.runningPods {
			response.GpuMetrics[uuid].PodMetrics[podName] = &metric.PodMetric{}
			response.GpuMetrics[uuid].PodMetrics[podName].MilliCpuUsed = podMetric.milliCPUUsed
			response.GpuMetrics[uuid].PodMetrics[podName].MemoryUsed = podMetric.memoryUsed
			response.GpuMetrics[uuid].PodMetrics[podName].StorageUsed = podMetric.storageUsed
			response.GpuMetrics[uuid].PodMetrics[podName].NetworkRx = podMetric.networkRX
			response.GpuMetrics[uuid].PodMetrics[podName].NetworkTx = podMetric.networkTX
		}
	}

	return response, nil
}

func (m *MetricCollector) StartGRPCServer(ctx context.Context) {
	lis, err := net.Listen("tcp", ":9323")
	if err != nil {
		klog.Fatalf("failed to listen: %v", err)
	}
	metricServer := grpc.NewServer()
	metric.RegisterMetricCollectorServer(metricServer, m)
	fmt.Println("-----:: Metric Collector Server Running... ::-----")
	if err := metricServer.Serve(lis); err != nil {
		klog.Fatalf("failed to serve: %v", err)
	}
}
