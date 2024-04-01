package collector

import (
	"context"
	"fmt"
	"gpu-metric-collector/pkg/api"
	"gpu-metric-collector/pkg/api/metric"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var GPU_METRIC_COLLECTOR_DEBUGG_LEVEL = os.Getenv("DEBUGG_LEVEL")
var MaxLanes = 16 //nvlink를 확인하기 위해 도는 레인 수

const (
	LEVEL1 = "LEVEL1"
	LEVEL2 = "LEVEL2"
	LEVEL3 = "LEVEL3"
)

type MetricCollector struct {
	metric.UnimplementedMetricCollectorServer
	HostKubeClient     *kubernetes.Clientset
	SafeMultiMetric    *SafeMultiMetric
	Interval           *time.Duration
	StatSummaryRequest *http.Request
}

type SafeMultiMetric struct {
	mutex       sync.Mutex
	MultiMetric *metric.MultiMetric
}

type NVLinkStatus struct {
	UUID          string
	BusID         string
	Lanes         map[string]int
	P2PUUID       []string
	P2PDeviceType []int //0 GPU, 1 IBMNPU, 2 SWITCH, 255 = UNKNOWN
	P2PBusID      []string
}

type Summary struct {
	Node NodeStats  `json:"node"`
	Pods []PodStats `json:"pods"`
}

type NodeStats struct {
	CPU     *CPUStats     `json:"cpu,omitempty"`
	Memory  *MemoryStats  `json:"memory,omitempty"`
	Network *NetworkStats `json:"network,omitempty"`
	Fs      *FsStats      `json:"fs,omitempty"`
}

type NetworkStats struct {
	Interfaces []InterfaceStats `json:"interfaces,omitempty"`
}

type PodStats struct {
	PodRef           PodReference  `json:"podRef"`
	CPU              *CPUStats     `json:"cpu,omitempty"`
	Memory           *MemoryStats  `json:"memory,omitempty"`
	Network          *NetworkStats `json:"network,omitempty"`
	EphemeralStorage *FsStats      `json:"ephemeral-storage,omitempty"`
}

type InterfaceStats struct {
	Name     string  `json:"name"`
	RxBytes  *uint64 `json:"rxBytes,omitempty"`
	RxErrors *uint64 `json:"rxErrors,omitempty"`
	TxBytes  *uint64 `json:"txBytes,omitempty"`
	TxErrors *uint64 `json:"txErrors,omitempty"`
}

type PodReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	UID       string `json:"uid"`
}

type CPUStats struct {
	Time                 v1.Time `json:"time"`
	UsageNanoCores       *uint64 `json:"usageNanoCores,omitempty"`
	UsageCoreNanoSeconds *uint64 `json:"usageCoreNanoSeconds,omitempty"`
}

type MemoryStats struct {
	Time            v1.Time `json:"time"`
	AvailableBytes  *uint64 `json:"availableBytes,omitempty"`
	UsageBytes      *uint64 `json:"usageBytes,omitempty"`
	WorkingSetBytes *uint64 `json:"workingSetBytes,omitempty"`
	RSSBytes        *uint64 `json:"rssBytes,omitempty"`
	PageFaults      *uint64 `json:"pageFaults,omitempty"`
	MajorPageFaults *uint64 `json:"majorPageFaults,omitempty"`
}

type FsStats struct {
	Time           v1.Time `json:"time"`
	AvailableBytes *uint64 `json:"availableBytes,omitempty"`
	CapacityBytes  *uint64 `json:"capacityBytes,omitempty"`
	UsedBytes      *uint64 `json:"usedBytes,omitempty"`
	InodesFree     *uint64 `json:"inodesFree,omitempty"`
	Inodes         *uint64 `json:"inodes,omitempty"`
	InodesUsed     *uint64 `json:"inodesUsed,omitempty"`
}

func NewMetricCollector() *MetricCollector {
	hostKubeClient := api.NewClientset()
	safeMultiMetric := NewSafeMultiMetric(hostKubeClient)
	interval := 5 * time.Second

	nodeIP := os.Getenv("NODE_IP")

	config, err := rest.InClusterConfig()
	if err != nil {
		KETI_LOG_L3("")
	}
	token := config.BearerToken

	scheme := "https"
	url := url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(nodeIP, strconv.Itoa(10250)),
		Path:   "/stats/summary",
	}

	statSummaryRequest, err := http.NewRequest("GET", url.String(), nil)
	if err != nil {
		KETI_LOG_L3("")
	}

	statSummaryRequest.Header.Set("Content-Type", "application/json")
	statSummaryRequest.Header.Set("Authorization", "Bearer "+token)

	return &MetricCollector{
		HostKubeClient:     hostKubeClient,
		SafeMultiMetric:    safeMultiMetric,
		Interval:           &interval,
		StatSummaryRequest: statSummaryRequest,
	}
}

func NewSafeMultiMetric(hostKubeClient *kubernetes.Clientset) *SafeMultiMetric {
	multiMetric, err := InitMultiMetric(hostKubeClient)
	if err != nil {
		log.Fatalf("new gauranteed multi metric error : %s", err)
	}

	return &SafeMultiMetric{
		MultiMetric: multiMetric,
	}
}

func NewMultiMetric(hostKubeClient *kubernetes.Clientset) *metric.MultiMetric {
	nodeName := os.Getenv("NODE_NAME")

	return &metric.MultiMetric{
		NodeName:   nodeName,
		GpuCount:   0,
		NvlinkInfo: make([]*metric.NVLink, 0),
		NodeMetric: InitNodeMetric(nodeName, hostKubeClient),
		GpuMetrics: make(map[string]*metric.GPUMetric),
		PodMetrics: make(map[string]*metric.PodMetric),
	}
}

func NewNodeMetric() *metric.NodeMetric {
	return &metric.NodeMetric{
		MilliCpuTotal: 0,
		MilliCpuUsage: 0,
		MemoryTotal:   0,
		MemoryUsage:   0,
		StorageTotal:  0,
		StorageUsage:  0,
		NetworkRx:     0,
		NetworkTx:     0,
	}
}

func NewGPUMetric() *metric.GPUMetric {
	return &metric.GPUMetric{
		Index:            0,
		GpuName:          "",
		Architecture:     "",
		MaxClock:         0,
		Cudacore:         0,
		Bandwidth:        0,
		Flops:            0,
		MaxOperativeTemp: 0,
		SlowdownTemp:     0,
		ShutdownTemp:     0,
		MemoryTotal:      0,
		MemoryUsed:       0,
		PowerUsed:        0,
		PciRx:            0,
		PciTx:            0,
		Temperature:      0,
		Utilization:      0,
		FanSpeed:         0,
		PodCount:         0,
	}
}

func NewPodMetric() *metric.PodMetric {
	return &metric.PodMetric{
		CpuUsage:      0,
		MemoryUsage:   0,
		StorageUsage:  0,
		NetworkRx:     0,
		NetworkTx:     0,
		IsGpuPod:      false,
		PodGpuMetrics: make(map[string]*metric.PodGPUMetric),
	}
}

func NewPodGPUMetric() *metric.PodGPUMetric {
	return &metric.PodGPUMetric{
		GpuUuid:       "",
		GpuProcessId:  "",
		GpuMemoryUsed: 0,
	}
}

func NewNVLink(s1 string, s2 string, l int32) *metric.NVLink {
	return &metric.NVLink{
		Gpu1Uuid:  s1,
		Gpu2Uuid:  s2,
		Lanecount: l,
	}
}

func InitMultiMetric(hostKubeClient *kubernetes.Clientset) (*metric.MultiMetric, error) {
	multiMetric := NewMultiMetric(hostKubeClient)

	nvmlReturn := nvml.Init()

	if nvmlReturn != nvml.SUCCESS {
		KETI_LOG_L3(fmt.Sprintf("[error] nvml.Init() not success %v: ", nvmlReturn))
		multiMetric.GpuCount = 0
		return multiMetric, nil
	} else {
		count, nvmlReturn := nvml.DeviceGetCount()
		if nvmlReturn != nvml.SUCCESS {
			KETI_LOG_L3(fmt.Sprintf("[error] nvml.DeviceGetCount() not success %v: ", nvmlReturn))
			multiMetric.GpuCount = 0
		} else {
			multiMetric.GpuCount = int64(count)

			for i := 0; i < count; i++ {
				device, ret := nvml.DeviceGetHandleByIndex(i)
				if ret != nvml.SUCCESS {
					KETI_LOG_L3(fmt.Sprintf("[error] unable to get device at index %d: %v", i, ret))
					continue
				}
				uuid, _ := device.GetUUID() //uuid
				index := int32(i)
				multiMetric.GpuMetrics[uuid] = InitGPUMetric(index)
			}

			var nvlinkStatus []NVLinkStatus
			for i := 0; i < count; i++ {
				nvlinkStatus = append(nvlinkStatus, NVLinkStatus{})
				device, _ := nvml.DeviceGetHandleByIndex(i)
				pciinfo, _ := device.GetPciInfo()
				tmparray := pciinfo.BusId

				var bytebus [32]byte
				for j := 0; j < 32; j++ {
					bytebus[j] = byte(tmparray[i])
				}

				nvlinkStatus[i].BusID = string(bytebus[:])
				nvlinkStatus[i].UUID, _ = device.GetUUID()
				nvlinkStatus[i].Lanes = make(map[string]int)

				for j := 0; j < MaxLanes; j++ { //Check nvlink by circling lanes as many as maxlane
					P2PPciInfo, err := device.GetNvLinkRemotePciInfo(j)
					if err != nvml.SUCCESS {
						break
					}
					tmparray := P2PPciInfo.BusId
					var bytebus [32]byte
					for k := 0; k < 32; k++ {
						bytebus[k] = byte(tmparray[k])
					}
					val, exists := nvlinkStatus[i].Lanes[string(bytebus[:])]
					if !exists {
						P2PDevice, err := nvml.DeviceGetHandleByPciBusId(string(bytebus[:]))
						if err != nvml.SUCCESS {
							KETI_LOG_L3("[error] error can get device handle")
						} else {
							P2PIndex, _ := P2PDevice.GetIndex()
							if P2PIndex > j {
								types, _ := device.GetNvLinkRemoteDeviceType(j)
								nvlinkStatus[i].Lanes[string(bytebus[:])] = 1
								nvlinkStatus[i].P2PDeviceType = append(nvlinkStatus[i].P2PDeviceType, int(types))
								P2PUUID, _ := P2PDevice.GetUUID()
								nvlinkStatus[i].P2PUUID = append(nvlinkStatus[i].P2PUUID, P2PUUID)
								nvlinkStatus[i].P2PBusID = append(nvlinkStatus[i].P2PBusID, string(bytebus[:]))
							}
						}

					} else {
						nvlinkStatus[i].Lanes[string(bytebus[:])] = val + 1
					}
				}

				for j := 0; j < len(nvlinkStatus[i].P2PUUID); j++ {
					nvlink := NewNVLink(nvlinkStatus[i].UUID, nvlinkStatus[i].P2PUUID[j], int32(nvlinkStatus[i].Lanes[nvlinkStatus[i].P2PBusID[j]]))
					multiMetric.NvlinkInfo = append(multiMetric.NvlinkInfo, nvlink)
				}
			}
		}
	}

	defer func() {
		ret := nvml.Shutdown()
		if ret != nvml.SUCCESS {
			log.Fatalf("Unable to shutdown NVML: %v", ret)
		}
	}()

	return multiMetric, nil
}

func InitNodeMetric(nodeName string, hostKubeClient *kubernetes.Clientset) *metric.NodeMetric {
	nodeMetric := NewNodeMetric()

	node, err := hostKubeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, v1.GetOptions{})
	if err != nil {
		log.Fatalf("cannot get node metric: %s", err)
	}

	nodeMetric.MemoryTotal, _ = node.Status.Capacity.Memory().AsInt64()
	nodeMetric.MilliCpuTotal = node.Status.Capacity.Cpu().MilliValue()
	nodeMetric.StorageTotal = node.Status.Capacity.StorageEphemeral().ToDec().MilliValue()

	return nodeMetric
}

func InitGPUMetric(i int32) *metric.GPUMetric {
	gpuMetric := NewGPUMetric()

	defer func() {
		ret := nvml.Shutdown()
		if ret != nvml.SUCCESS {
			log.Fatalf("Unable to shutdown NVML: %v", ret)
		}
	}()

	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		log.Fatalf("Unable nvml.Init(): %v", ret)
	}

	device, ret := nvml.DeviceGetHandleByIndex(int(gpuMetric.Index))
	if ret != nvml.SUCCESS {
		log.Fatalf("[error] unable to get device at index %d: %v", gpuMetric.Index, ret)
	}

	gpuMetric.Index = i

	gpuMetric.GpuName, ret = device.GetName()
	if ret != nvml.SUCCESS {
		KETI_LOG_L2(fmt.Sprintf("[error] unable device.GetName(): %v", ret))
	}

	deviceArchitecture, ret := device.GetArchitecture()
	if ret != nvml.SUCCESS {
		KETI_LOG_L2(fmt.Sprintf("[error] unable device.GetArchitecture(): %v", ret))
	}
	switch deviceArchitecture {
	case nvml.DEVICE_ARCH_KEPLER:
		gpuMetric.Architecture = "KEPLER"
	case nvml.DEVICE_ARCH_MAXWELL:
		gpuMetric.Architecture = "MAXWELL"
	case nvml.DEVICE_ARCH_PASCAL:
		gpuMetric.Architecture = "PASCAL"
	case nvml.DEVICE_ARCH_VOLTA:
		gpuMetric.Architecture = "VOLTA"
	case nvml.DEVICE_ARCH_TURING:
		gpuMetric.Architecture = "TURING"
	case nvml.DEVICE_ARCH_AMPERE:
		gpuMetric.Architecture = "AMPERE"
	case nvml.DEVICE_ARCH_HOPPER:
		gpuMetric.Architecture = "HOPPER"
	default:
		gpuMetric.Architecture = "Unknown"
	}

	/*
		DEVICE_ARCH_KEPLER = 2
		DEVICE_ARCH_MAXWELL = 3
		DEVICE_ARCH_PASCAL = 4
		DEVICE_ARCH_VOLTA = 5
		DEVICE_ARCH_TURING = 6
		DEVICE_ARCH_AMPERE = 7
		DEVICE_ARCH_ADA = 8
		DEVICE_ARCH_HOPPER = 9
		DEVICE_ARCH_UNKNOWN = 4294967295
	*/

	maxClock, ret := device.GetMaxClockInfo(0)
	if ret != nvml.SUCCESS {
		KETI_LOG_L2(fmt.Sprintf("[error] unable device.GetMaxClockInfo(0): %v", ret))
	}
	gpuMetric.MaxClock = int64(maxClock)

	cudacore, ret := device.GetNumGpuCores()
	if ret != nvml.SUCCESS {
		KETI_LOG_L2(fmt.Sprintf("[error] unable device.GetNumGpuCores(): %v", ret))
	}
	gpuMetric.Cudacore = int64(cudacore)

	buswidth, ret := device.GetMemoryBusWidth()
	if ret != nvml.SUCCESS {
		KETI_LOG_L2(fmt.Sprintf("[error] unable device.GetMemoryBusWidth(): %v", ret))
	}

	gpuMetric.Bandwidth = float32(buswidth) * float32(gpuMetric.MaxClock) * 2 / 8 / 1e6
	gpuMetric.Flops = int64(gpuMetric.MaxClock * gpuMetric.Cudacore * 2 / 1000)

	maxOperativeTemp, ret := device.GetTemperatureThreshold(nvml.TEMPERATURE_THRESHOLD_GPU_MAX)
	if ret != nvml.SUCCESS {
		KETI_LOG_L2(fmt.Sprintf("[error] unable device.GetTemperatureThreshold(nvml.TEMPERATURE_THRESHOLD_GPU_MAX): %v", ret))
	}
	gpuMetric.MaxOperativeTemp = int64(maxOperativeTemp)

	slowdownTemp, ret := device.GetTemperatureThreshold(nvml.TEMPERATURE_THRESHOLD_SLOWDOWN)
	if ret != nvml.SUCCESS {
		KETI_LOG_L2(fmt.Sprintf("[error] unable device.GetTemperatureThreshold(nvml.TEMPERATURE_THRESHOLD_SLOWDOWN): %v", ret))
	}
	gpuMetric.SlowdownTemp = int64(slowdownTemp)

	shutdownTemp, ret := device.GetTemperatureThreshold(nvml.TEMPERATURE_THRESHOLD_SHUTDOWN)
	if ret != nvml.SUCCESS {
		KETI_LOG_L2(fmt.Sprintf("[error] unable device.GetTemperatureThreshold(nvml.TEMPERATURE_THRESHOLD_SHUTDOWN): %v", ret))
	}
	gpuMetric.ShutdownTemp = int64(shutdownTemp)

	memory, ret := device.GetMemoryInfo()
	if ret != nvml.SUCCESS {
		KETI_LOG_L2(fmt.Sprintf("[error] unable device.GetMemoryInfo(): %v", ret))
	}
	gpuMetric.MemoryTotal = int64(memory.Total)

	return gpuMetric
}

func DumpMultiMetric(multiMetric *metric.MultiMetric) {
	KETI_LOG_L1("\n---:: Dump Metric Collector ::---")

	KETI_LOG_L1("1. [Multi Metric]")
	KETI_LOG_L1(fmt.Sprintf("1-1. node name : %s", multiMetric.NodeName))
	KETI_LOG_L1(fmt.Sprintf("1-2. gpu count : %d", multiMetric.GpuCount))
	if len(multiMetric.NvlinkInfo) != 0 {
		KETI_LOG_L3("1-3. gpu nvlink connected : true")
		for i, nvlink := range multiMetric.NvlinkInfo {
			KETI_LOG_L1(fmt.Sprintf(" - link %d gpu a : %s", i, nvlink.Gpu1Uuid))
			KETI_LOG_L1(fmt.Sprintf(" - link %d gpu b : %s", i, nvlink.Gpu2Uuid))
			KETI_LOG_L1(fmt.Sprintf(" - link %d lane : %d", i, nvlink.Lanecount))
		}
	} else {
		KETI_LOG_L3("1-3. gpu nvlink connected : false")
	}

	KETI_LOG_L1("2. [Node Metric]")
	KETI_LOG_L1(fmt.Sprintf("2-1. node cpu (used/total) : %d/%d (milli core)", multiMetric.NodeMetric.MilliCpuUsage, multiMetric.NodeMetric.MilliCpuTotal))
	KETI_LOG_L1(fmt.Sprintf("2-2. node memory (used/total) : %d/%d (byte)", multiMetric.NodeMetric.MemoryUsage, multiMetric.NodeMetric.MemoryTotal))
	KETI_LOG_L1(fmt.Sprintf("2-3. node storage (used/total) : %d/%d (byte)", multiMetric.NodeMetric.StorageUsage, multiMetric.NodeMetric.StorageTotal))
	KETI_LOG_L1(fmt.Sprintf("2-4. node network (rx/tx) : %d/%d (byte)", multiMetric.NodeMetric.NetworkRx, multiMetric.NodeMetric.NetworkTx))

	KETI_LOG_L1("3. [GPU Metric]")
	for gpuName, gpuMetric := range multiMetric.GpuMetrics {
		KETI_LOG_L1(fmt.Sprintf("3-0. GPU UUID : %s", gpuName))
		KETI_LOG_L1(fmt.Sprintf("3-1. index : %d", gpuMetric.Index))
		KETI_LOG_L1(fmt.Sprintf("3-2. gpu name : %s", gpuMetric.GpuName))
		KETI_LOG_L1(fmt.Sprintf("3-3. architecture : %s", gpuMetric.Architecture))
		KETI_LOG_L1(fmt.Sprintf("3-4. max clock : %d", gpuMetric.MaxClock))
		KETI_LOG_L1(fmt.Sprintf("3-5. cudacore : %d", gpuMetric.Cudacore))
		KETI_LOG_L1(fmt.Sprintf("3-6. bandwidth : %f", gpuMetric.Bandwidth))
		KETI_LOG_L1(fmt.Sprintf("3-7. flops : %d", gpuMetric.Flops))
		KETI_LOG_L1(fmt.Sprintf("3-8. max operative temperature : %d", gpuMetric.MaxOperativeTemp))
		KETI_LOG_L1(fmt.Sprintf("3-9. slow down temperature : %d", gpuMetric.SlowdownTemp))
		KETI_LOG_L1(fmt.Sprintf("3-10. shut dowm temperature : %d", gpuMetric.ShutdownTemp))
		KETI_LOG_L1(fmt.Sprintf("3-11. memory (used/total) : %d/%d", gpuMetric.MemoryUsed, gpuMetric.MemoryTotal))
		KETI_LOG_L1(fmt.Sprintf("3-12. power (used) : %d", gpuMetric.PowerUsed))
		KETI_LOG_L1(fmt.Sprintf("3-13. pci (rx/tx) :  %d/%d", gpuMetric.PciRx, gpuMetric.PciTx))
		KETI_LOG_L1(fmt.Sprintf("3-14. temperature : %d", gpuMetric.Temperature))
		KETI_LOG_L1(fmt.Sprintf("3-15. utilization : %d", gpuMetric.Utilization))
		KETI_LOG_L1(fmt.Sprintf("3-16. fan speed : %d", gpuMetric.FanSpeed))
		KETI_LOG_L1(fmt.Sprintf("3-17. energy comsumption : %d", gpuMetric.EnergyConsumption))
		KETI_LOG_L1(fmt.Sprintf("3-18. pod count : %d", gpuMetric.PodCount))
	}

	KETI_LOG_L1("4. [Pod Metric]")
	for podName, podMetric := range multiMetric.PodMetrics {
		KETI_LOG_L1(fmt.Sprintf("# Pod Name : %s", podName))
		KETI_LOG_L1(fmt.Sprintf("4-1. pod milli cpu (used) : %d", podMetric.CpuUsage))
		KETI_LOG_L1(fmt.Sprintf("4-2. pod memory (used) : %d", podMetric.MemoryUsage))
		KETI_LOG_L1(fmt.Sprintf("4-3. pod storage (used) : %d", podMetric.StorageUsage))
		KETI_LOG_L1(fmt.Sprintf("4-4. pod network (rx/tx) :  %d/%d", podMetric.NetworkRx, podMetric.NetworkTx))
		for _, podGPUMetric := range podMetric.PodGpuMetrics {
			KETI_LOG_L1(fmt.Sprintf("# GPU UUID : %s", podGPUMetric.GpuUuid))
			KETI_LOG_L1(fmt.Sprintf("4-5. gpu process id :  %s", podGPUMetric.GpuProcessId))
			KETI_LOG_L1(fmt.Sprintf("4-6. gpu memory :  %d", podGPUMetric.GpuMemoryUsed))
		}
	}
	KETI_LOG_L1("-----------------------------------\n")
}

func DumpMultiMetricForTest(multiMetric *metric.MultiMetric) {
	KETI_LOG_L3("\n---:: KETI GPU Metric Collector Status ::---")

	KETI_LOG_L3(fmt.Sprintf("# Node Name : %s", multiMetric.NodeName))
	KETI_LOG_L3(fmt.Sprintf("[Metric #01] node milli cpu (used/total) : %d/%d (milli core)", multiMetric.NodeMetric.MilliCpuUsage, multiMetric.NodeMetric.MilliCpuTotal))
	KETI_LOG_L3(fmt.Sprintf("[Metric #02] node memory (used/total) : %d/%d (byte)", multiMetric.NodeMetric.MemoryUsage, multiMetric.NodeMetric.MemoryTotal))
	KETI_LOG_L3(fmt.Sprintf("[Metric #03] node storage (used/total) : %d/%d (byte)", multiMetric.NodeMetric.StorageUsage, multiMetric.NodeMetric.StorageTotal))
	KETI_LOG_L3(fmt.Sprintf("[Metric #04] node network rx : %d (byte)", multiMetric.NodeMetric.NetworkRx))
	KETI_LOG_L3(fmt.Sprintf("[Metric #05] node network tx : %d (byte)", multiMetric.NodeMetric.NetworkTx))

	if len(multiMetric.NvlinkInfo) != 0 {
		KETI_LOG_L3("[Metric #06] gpu nvlink connected : true")
		for i, nvlink := range multiMetric.NvlinkInfo {
			KETI_LOG_L1(fmt.Sprintf("- link %d gpu a : %s", i, nvlink.Gpu1Uuid))
			KETI_LOG_L1(fmt.Sprintf("- link %d gpu b : %s", i, nvlink.Gpu2Uuid))
			KETI_LOG_L1(fmt.Sprintf("- link %d lane : %d", i, nvlink.Lanecount))
		}
	} else {
		KETI_LOG_L3("[Metric #06] gpu nvlink connected : false")
	}

	for gpuName, gpuMetric := range multiMetric.GpuMetrics {
		KETI_LOG_L3(fmt.Sprintf("# GPU UUID : %s", gpuName))
		fmt.Println("[Metric #07] gpu name : ", gpuMetric.GpuName)
		fmt.Println("[Metric #08] gpu architecture : ", gpuMetric.Architecture)
		fmt.Println("[Metric #09] gpu max clock (MHz) : ", gpuMetric.MaxClock)
		fmt.Println("[Metric #10] gpu cudacore : ", gpuMetric.Cudacore)
		fmt.Println("[Metric #11] gpu bandwidth (GB/s) : ", gpuMetric.Bandwidth)
		fmt.Println("[Metric #12] gpu flops : ", gpuMetric.Flops)
		fmt.Println("[Metric #13] gpu max operative temperature (celsius) : ", gpuMetric.MaxOperativeTemp)
		fmt.Println("[Metric #14] gpu slow down temperature (celsius) : ", gpuMetric.SlowdownTemp)
		fmt.Println("[Metric #15] gpu shut dowm temperature (celsius) : ", gpuMetric.ShutdownTemp)
		fmt.Println("[Metric #16] gpu memory used (byte) : ", gpuMetric.MemoryUsed)
		fmt.Println("[Metric #17] gpu power used (watt) : ", gpuMetric.PowerUsed)
		fmt.Println("[Metric #18] gpu temperature (celsius) : ", gpuMetric.Temperature)
		fmt.Println("[Metric #19] gpu memory utilization (%) : ", gpuMetric.Utilization)
		fmt.Println("[Metric #20] gpu energy consumption (kw/h) : ", gpuMetric.EnergyConsumption)
	}

	KETI_LOG_L3("----------------------------------------------\n")
}

func KETI_LOG_L1(log string) { //자세한 출력, DumpClusterInfo DumpNodeInfo
	if GPU_METRIC_COLLECTOR_DEBUGG_LEVEL == LEVEL1 {
		fmt.Println(log)
	}
}

func KETI_LOG_L2(log string) { // 기본출력
	if GPU_METRIC_COLLECTOR_DEBUGG_LEVEL == LEVEL1 || GPU_METRIC_COLLECTOR_DEBUGG_LEVEL == LEVEL2 {
		fmt.Println(log)
	}
}

func KETI_LOG_L3(log string) { //필수출력, 정량용, 에러
	if GPU_METRIC_COLLECTOR_DEBUGG_LEVEL == LEVEL1 || GPU_METRIC_COLLECTOR_DEBUGG_LEVEL == LEVEL2 || GPU_METRIC_COLLECTOR_DEBUGG_LEVEL == LEVEL3 {
		fmt.Println(log)
	}
}
