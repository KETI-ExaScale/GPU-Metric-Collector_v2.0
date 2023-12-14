package collector

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"gpu-metric-collector/pkg/api/metric"
	"io/ioutil"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
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

	if summary, err := m.getStats(); err == nil {
		m.SafeMultiMetric.MultiMetric.NodeMetric.MilliCpuUsage = int64(math.Ceil(float64(*summary.Node.CPU.UsageNanoCores) / 1000000))
		m.SafeMultiMetric.MultiMetric.NodeMetric.MemoryUsage = int64(*summary.Node.Memory.UsageBytes)
		m.SafeMultiMetric.MultiMetric.NodeMetric.StorageUsage = int64(*summary.Node.Fs.UsedBytes)

		var RX_Usage uint64 = 0
		var TX_Usage uint64 = 0

		for _, Interface := range summary.Node.Network.Interfaces {
			RX_Usage = RX_Usage + *Interface.RxBytes
			TX_Usage = TX_Usage + *Interface.TxBytes
		}

		var networkRXBytes resource.Quantity
		var networkTXBytes resource.Quantity

		networkRXBytes = *uint64Quantity(RX_Usage, 0)
		networkRXBytes.Format = resource.BinarySI

		networkTXBytes = *uint64Quantity(TX_Usage, 0)
		networkTXBytes.Format = resource.BinarySI

		m.SafeMultiMetric.MultiMetric.NodeMetric.NetworkRx, _ = networkRXBytes.AsInt64()
		m.SafeMultiMetric.MultiMetric.NodeMetric.NetworkTx, _ = networkTXBytes.AsInt64()

		for _, pod := range summary.Pods {
			podMetric := NewPodMetric()
			podName := pod.PodRef.Name

			if pod.CPU.UsageNanoCores != nil {
				podMetric.CpuUsage = int64(math.Ceil(float64(*pod.CPU.UsageNanoCores) / 1000000))
			}

			if pod.Memory.UsageBytes != nil {
				podMetric.MemoryUsage = int64(*pod.Memory.UsageBytes)
			}

			if pod.EphemeralStorage.UsedBytes != nil {
				podMetric.StorageUsage = int64(*pod.EphemeralStorage.UsedBytes)
			}

			if pod.Network != nil {
				var RX_Usage uint64 = 0
				var TX_Usage uint64 = 0

				for _, Interface := range pod.Network.Interfaces {
					RX_Usage = RX_Usage + *Interface.RxBytes
					TX_Usage = TX_Usage + *Interface.TxBytes
				}

				var networkRXBytes resource.Quantity
				var networkTXBytes resource.Quantity

				networkRXBytes = *uint64Quantity(RX_Usage, 0)
				networkRXBytes.Format = resource.BinarySI

				networkTXBytes = *uint64Quantity(TX_Usage, 0)
				networkTXBytes.Format = resource.BinarySI

				podMetric.NetworkRx, _ = networkRXBytes.AsInt64()
				podMetric.NetworkTx, _ = networkTXBytes.AsInt64()
			}

			m.SafeMultiMetric.MultiMetric.PodMetrics[podName] = podMetric
		}
	}

	if m.SafeMultiMetric.MultiMetric.GpuCount != 0 {
		defer func() {
			ret := nvml.Shutdown()
			if ret != nvml.SUCCESS {
				KETI_LOG_L2(fmt.Sprintf("[error] unable to shutdown NVML: %v", ret))
			}
		}()

		podGPUMetrics := make(map[string]*metric.PodGPUMetric)

		for uuid, gpuMetric := range m.SafeMultiMetric.MultiMetric.GpuMetrics {
			ret := nvml.Init()
			if ret != nvml.SUCCESS {
				KETI_LOG_L2(fmt.Sprintf("[error] unable nvml.Init(): %v", ret))
			}

			device, ret := nvml.DeviceGetHandleByUUID(uuid)
			if ret != nvml.SUCCESS {
				KETI_LOG_L2(fmt.Sprintf("[error] unable to get device at uuid : %v", ret))
			}

			memory, ret := device.GetMemoryInfo()
			gpuMetric.MemoryUsed = int64(memory.Used)
			if ret != nvml.SUCCESS {
				KETI_LOG_L2(fmt.Sprintf("[error] unable to get memory info : %v", ret))
			}

			power, ret := device.GetPowerUsage()
			gpuMetric.PowerUsed = int64(power)
			if ret != nvml.SUCCESS {
				KETI_LOG_L2(fmt.Sprintf("[error] unable to get power info : %v", ret))
			}

			picTX, ret := device.GetPcieThroughput(0)
			gpuMetric.PciTx = int64(picTX)
			if ret != nvml.SUCCESS {
				KETI_LOG_L2(fmt.Sprintf("[error] unable to get pci pci xt info : %v", ret))
			}

			pciRX, ret := device.GetPcieThroughput(1)
			gpuMetric.PciRx = int64(pciRX)
			if ret != nvml.SUCCESS {
				KETI_LOG_L2(fmt.Sprintf("[error] unable to get pci rx info : %v", ret))
			}

			temperature, ret := device.GetTemperature(0)
			gpuMetric.Temperature = int64(temperature)
			if ret != nvml.SUCCESS {
				KETI_LOG_L2(fmt.Sprintf("[error] unable to get temperature info : %v", ret))
			}

			utilization, ret := device.GetUtilizationRates()
			gpuMetric.Utilization = int64(utilization.Gpu)
			if ret != nvml.SUCCESS {
				KETI_LOG_L2(fmt.Sprintf("[error] unable to get utilization info : %v", ret))
			}

			fanSpeed, ret := device.GetFanSpeed()
			gpuMetric.FanSpeed = int64(fanSpeed)
			if ret != nvml.SUCCESS {
				KETI_LOG_L2(fmt.Sprintf("[error] unable to get fanspeed info : %v", ret))
			}

			energy, ret := device.GetTotalEnergyConsumption()
			gpuMetric.EnergyConsumption = int64(energy)
			if ret != nvml.SUCCESS {
				KETI_LOG_L2(fmt.Sprintf("[error] unable to get total energy consumption : %v", ret))
			}

			var nvmlProcess []nvml.ProcessInfo
			nvmlProcess, ret = device.GetMPSComputeRunningProcesses()
			if ret != nvml.SUCCESS {
				KETI_LOG_L2(fmt.Sprintf("[error] unable to get nvml process : %v", ret))
			}

			// get gpu kubnernetes process info
			for _, process := range nvmlProcess {
				pid := strconv.FormatUint(uint64(process.Pid), 10)

				cgroupfile, err := os.Open("/proc/" + pid + "/cgroup")
				if err != nil {
					KETI_LOG_L2(fmt.Sprintf("[error] cannot open /proc/%s/cgroup file : %v", pid, err))
				}
				cgroupscanner := bufio.NewScanner(cgroupfile)

				podGPUMetric := NewPodGPUMetric()
				podContainerID := ""

				if cgroupscanner.Scan() {
					cgroup := cgroupscanner.Text()
					cgroups := strings.Split(cgroup, "/")

					if strings.Contains(cgroups[1], "kubepods") || cgroups[1] == "kubepods.slice" {
						gpuMetric.PodCount++
						podGPUMetric.GpuUuid = uuid
						podGPUMetric.GpuProcessId = pid
						podGPUMetric.GpuMemoryUsed = int64(process.UsedGpuMemory)

						if len(cgroups) > 4 {
							tmpslice := strings.Split(cgroups[4], "-")
							if len(tmpslice) > 1 {
								tmpslice1 := strings.Split(tmpslice[1], ".")
								podContainerID = tmpslice1[0]
							} else {
								podContainerID = cgroups[4]
							}
						} else {
							podContainerID = ""
						}

					}
					// break // for문으로 전체 돌면서 뭐지?/
					fmt.Println("PodContainerID:", podContainerID)
				}
				podGPUMetrics[podContainerID] = podGPUMetric
			}
		}

		// pod container id <-> gpu process pid mapping
		selector := fields.SelectorFromSet(fields.Set{
			"spec.nodeName":      m.SafeMultiMetric.MultiMetric.NodeName,
			"status.phase":       "Running",
			"spec.schedulerName": "gpu-scheduler",
		})
		podlist, err := m.HostKubeClient.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{
			FieldSelector: selector.String(),
		})

		if err != nil {
			KETI_LOG_L2(fmt.Sprintf("[error] unable to get pods: %v", err))
		}

		for _, podItem := range podlist.Items {
			podName := podItem.Name
			for _, podContainer := range podItem.Status.ContainerStatuses {
				containerID := podContainer.ContainerID
				trimmedcontainerID := strings.TrimPrefix(containerID, "docker://")
				fmt.Println("container ID:", containerID, "trimmedContainer ID:", trimmedcontainerID)
				fmt.Println("Input GPU Metric, PodName: ", podName)
				if podGPUMetric, ok := podGPUMetrics[trimmedcontainerID]; ok {
					m.SafeMultiMetric.MultiMetric.PodMetrics[podName].PodGpuMetrics[trimmedcontainerID] = podGPUMetric
					m.SafeMultiMetric.MultiMetric.PodMetrics[podName].IsGpuPod = true
				}
			}
		}

	}

	DumpMultiMetric(m.SafeMultiMetric.MultiMetric) // DEBUGG LEVEL = 1 일때 출력 수행
	// DumpMultiMetricForTest(m.SafeMultiMetric.MultiMetric) // 항상 (정량) 출력 수행
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

// func (m *MetricCollector) getNodeNetworkStats() (*Network, error) {
// 	transport := &http.Transport{
// 		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
// 	}

// 	client := &http.Client{
// 		Transport: transport,
// 	}

// 	response, err := client.Do(m.StatSummaryRequest)
// 	if err != nil {
// 		KETI_LOG_L3(fmt.Sprintf("[error] get node network stats error: %v", err))
// 		return nil, err
// 	}

// 	defer response.Body.Close()

// 	body, err := ioutil.ReadAll(response.Body)
// 	if err != nil {
// 		KETI_LOG_L3(fmt.Sprintf("[error] get node network stats error: %v", err))
// 		return nil, err
// 	}

// 	summary := &Summary{}

// 	if err := json.Unmarshal(body, &summary); err != nil {
// 		KETI_LOG_L3(fmt.Sprintf("[error] get node network stats error: %v", err))
// 		return nil, err
// 	}

// 	var RX_Usage uint64 = 0
// 	var TX_Usage uint64 = 0

// 	for _, Interface := range summary.Node.Network.Interfaces {
// 		RX_Usage = RX_Usage + *Interface.RxBytes
// 		TX_Usage = TX_Usage + *Interface.TxBytes
// 	}

// 	network := &Network{}

// 	network.NetworkRxBytes = *uint64Quantity(RX_Usage, 0)
// 	network.NetworkRxBytes.Format = resource.BinarySI

// 	network.NetworkTxBytes = *uint64Quantity(TX_Usage, 0)
// 	network.NetworkTxBytes.Format = resource.BinarySI

// 	return network, nil
// }

func (m *MetricCollector) getStats() (*Summary, error) {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	client := &http.Client{
		Transport: transport,
	}

	response, err := client.Do(m.StatSummaryRequest)
	if err != nil {
		return nil, fmt.Errorf("[error] get node network stats error: %v", err)
	}

	defer response.Body.Close()

	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("[error] get node network stats error: %v", err)
	}

	summary := &Summary{}

	if err := json.Unmarshal(body, &summary); err != nil {
		return nil, fmt.Errorf("[error] get node network stats error: %v", err)
	}

	return summary, nil
}

func uint64Quantity(val uint64, scale resource.Scale) *resource.Quantity {
	if val <= math.MaxInt64 {
		return resource.NewScaledQuantity(int64(val), scale)
	}

	return resource.NewScaledQuantity(int64(val/10), resource.Scale(1)+scale)
}
