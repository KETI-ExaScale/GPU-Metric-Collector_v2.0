package collector

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"gpu-metric-collector/pkg/api/metric"
	"io/ioutil"
	"log"
	"math"
	"net"
	"net/http"
	"sync"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

func (m *MetricCollector) RunMetricCollector(ctx context.Context, wg *sync.WaitGroup) {
	go m.StartGRPCServer(ctx, wg)
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
	m.SafeMultiMetric.MultiMetric.NodeMetric.StorageFree, _ = node.Status.Allocatable.StorageEphemeral().AsInt64()

	if network, err := m.getNodeNetworkStats(); err != nil {
		m.SafeMultiMetric.MultiMetric.NodeMetric.NetworkRx = 0
		m.SafeMultiMetric.MultiMetric.NodeMetric.NetworkTx = 0
	} else {
		m.SafeMultiMetric.MultiMetric.NodeMetric.NetworkRx, _ = network.NetworkRxBytes.AsInt64()
		m.SafeMultiMetric.MultiMetric.NodeMetric.NetworkTx, _ = network.NetworkTxBytes.AsInt64()
	}

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

func (m *MetricCollector) StartGRPCServer(ctx context.Context, wg *sync.WaitGroup) {
	lis, err := net.Listen("tcp", ":9323")
	if err != nil {
		klog.Fatalf("failed to listen: %v", err)
	}

	metricServer := grpc.NewServer()
	metric.RegisterMetricCollectorServer(metricServer, m)

	KETI_LOG_L3("[gRPC] metric collector server running...")

	go func() {
		if err := metricServer.Serve(lis); err != nil {
			klog.Fatalf("failed to serve: %v", err)
		}
	}()

	<-ctx.Done()
	wg.Done()
}

func (m *MetricCollector) getNodeNetworkStats() (*Network, error) {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	client := &http.Client{
		Transport: transport,
	}

	response, err := client.Do(m.NetworkRequest)
	if err != nil {
		KETI_LOG_L3(fmt.Sprintf("[error] get node network stats error: %v", err))
		return nil, err
	}

	defer response.Body.Close()

	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		KETI_LOG_L3(fmt.Sprintf("[error] get node network stats error: %v", err))
		return nil, err
	}

	summary := &Summary{}

	if err := json.Unmarshal(body, &summary); err != nil {
		KETI_LOG_L3(fmt.Sprintf("[error] get node network stats error: %v", err))
		return nil, err
	}

	var RX_Usage uint64 = 0
	var TX_Usage uint64 = 0

	for _, Interface := range summary.Node.Network.Interfaces {
		RX_Usage = RX_Usage + *Interface.RxBytes
		TX_Usage = TX_Usage + *Interface.TxBytes
	}

	network := &Network{}

	network.NetworkRxBytes = *uint64Quantity(RX_Usage, 0)
	network.NetworkRxBytes.Format = resource.BinarySI

	network.NetworkTxBytes = *uint64Quantity(TX_Usage, 0)
	network.NetworkTxBytes.Format = resource.BinarySI

	return network, nil
}

func uint64Quantity(val uint64, scale resource.Scale) *resource.Quantity {
	if val <= math.MaxInt64 {
		return resource.NewScaledQuantity(int64(val), scale)
	}

	return resource.NewScaledQuantity(int64(val/10), resource.Scale(1)+scale)
}
