package infra

import "time"

type Snapshot struct {
	CollectedAt     time.Time `json:"collected_at"`
	IntervalSeconds int       `json:"interval_seconds"`

	Host     Host            `json:"host"`
	Runtime  Runtime         `json:"runtime"`
	Kinds    []KindUsage     `json:"kinds"`
	Octopus  Octopus         `json:"octopus"`
	Storage  Storage         `json:"storage"`
	Problems []Problem       `json:"problems"`
	Totals   ContainerTotals `json:"totals"`
}

type Problem struct {
	Source  string `json:"source"`
	Message string `json:"message"`
	Err     error  `json:"-"`
}

// In a container, CPU, memory and load are the host's (the kernel does not namespace /proc/stat or
// /proc/meminfo), but network and mounts are the container's own.
type Host struct {
	Hostname       string    `json:"hostname"`
	OS             string    `json:"os"`
	Kernel         string    `json:"kernel"`
	Arch           string    `json:"arch"`
	UptimeSeconds  float64   `json:"uptime_seconds"`
	CPUModel       string    `json:"cpu_model"`
	Cores          int       `json:"cores"`
	Threads        int       `json:"threads"`
	CPUPercent     float64   `json:"cpu_percent"`
	Load           []float64 `json:"load"`
	Memory         Memory    `json:"memory"`
	Net            Net       `json:"net"`
	Disks          []Disk    `json:"disks"`
	InContainer    bool      `json:"in_container"`
	ProcAvailable  bool      `json:"proc_available"`
	NetInContainer bool      `json:"net_in_container"`
}

type Memory struct {
	Total     uint64  `json:"total"`
	Used      uint64  `json:"used"`
	Available uint64  `json:"available"`
	Cached    uint64  `json:"cached"`
	Buffers   uint64  `json:"buffers"`
	SwapTotal uint64  `json:"swap_total"`
	SwapUsed  uint64  `json:"swap_used"`
	Percent   float64 `json:"percent"`
}

type Net struct {
	RxBps      float64        `json:"rx_bps"`
	TxBps      float64        `json:"tx_bps"`
	Interfaces []NetInterface `json:"interfaces"`
}

type NetInterface struct {
	Name    string  `json:"name"`
	RxBps   float64 `json:"rx_bps"`
	TxBps   float64 `json:"tx_bps"`
	RxBytes uint64  `json:"rx_bytes"`
	TxBytes uint64  `json:"tx_bytes"`
}

type Disk struct {
	Path          string   `json:"path"`
	Roles         []string `json:"roles"`
	Size          uint64   `json:"size"`
	Used          uint64   `json:"used"`
	Free          uint64   `json:"free"`
	Percent       float64  `json:"percent"`
	InodesTotal   uint64   `json:"inodes_total"`
	InodesUsed    uint64   `json:"inodes_used"`
	InodesPercent float64  `json:"inodes_percent"`
}

type Runtime struct {
	Mode   string      `json:"mode"`
	Docker *DockerInfo `json:"docker,omitempty"`
	Kube   *KubeInfo   `json:"kube,omitempty"`
}

type DockerInfo struct {
	ServerVersion   string `json:"server_version"`
	OperatingSystem string `json:"operating_system"`
	KernelVersion   string `json:"kernel_version"`
	NCPU            int    `json:"ncpu"`
	MemTotal        int64  `json:"mem_total"`
	StorageDriver   string `json:"storage_driver"`
	RootDir         string `json:"root_dir"`
	ComposeProject  string `json:"compose_project"`

	Containers int `json:"containers"`
	Running    int `json:"running"`
	Paused     int `json:"paused"`
	Stopped    int `json:"stopped"`

	DiskUsageAt    time.Time    `json:"disk_usage_at,omitzero"`
	Images         int          `json:"images"`
	ImagesSize     int64        `json:"images_size"`
	VolumesSize    int64        `json:"volumes_size"`
	BuildCacheSize int64        `json:"build_cache_size"`
	Volumes        []VolumeSize `json:"volumes"`
}

// Size is -1 when Docker did not compute it.
type VolumeSize struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type KubeInfo struct {
	RuntimeClass          string        `json:"runtime_class"`
	ControlPlaneNamespace string        `json:"control_plane_namespace"`
	MetricsAvailable      bool          `json:"metrics_available"`
	Nodes                 []Node        `json:"nodes"`
	Quotas                []Quota       `json:"quotas"`
	PVCs                  []PVC         `json:"pvcs"`
	Pods                  PodPhaseTally `json:"pods"`
	// Empty when the install does not build its agent image (ee/kuberuntime imagebuild.go).
	AgentImages []AgentImageBuild `json:"agent_images"`
}

// AgentImageBuild is one published agent image and the image the install derived from it with
// Claude Code installed. State is pending, building, ready, failed or skipped; Message (localized
// from Err) says why for failed and skipped.
type AgentImageBuild struct {
	Base    string    `json:"base"`
	Image   string    `json:"image,omitempty"`
	State   string    `json:"state"`
	Job     string    `json:"job,omitempty"`
	Message string    `json:"message,omitempty"`
	Err     error     `json:"-"`
	Since   time.Time `json:"since"`
}

type PodPhaseTally struct {
	Total     int `json:"total"`
	Running   int `json:"running"`
	Pending   int `json:"pending"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Unknown   int `json:"unknown"`
}

type Node struct {
	Name             string          `json:"name"`
	KubeletVersion   string          `json:"kubelet_version"`
	OSImage          string          `json:"os_image"`
	Kernel           string          `json:"kernel"`
	ContainerRuntime string          `json:"container_runtime"`
	CPUCapacityMilli int64           `json:"cpu_capacity_milli"`
	CPUAllocMilli    int64           `json:"cpu_allocatable_milli"`
	MemCapacity      int64           `json:"mem_capacity"`
	MemAlloc         int64           `json:"mem_allocatable"`
	EphemeralCap     int64           `json:"ephemeral_capacity"`
	PodsCapacity     int64           `json:"pods_capacity"`
	CPUUsageMilli    int64           `json:"cpu_usage_milli"`
	MemUsage         int64           `json:"mem_usage"`
	CPURequestsMilli int64           `json:"cpu_requests_milli"`
	MemRequests      int64           `json:"mem_requests"`
	Conditions       []NodeCondition `json:"conditions"`
}

type NodeCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type Quota struct {
	Namespace     string            `json:"namespace"`
	Name          string            `json:"name"`
	WorkspaceID   string            `json:"workspace_id"`
	WorkspaceName string            `json:"workspace_name"`
	Hard          map[string]string `json:"hard"`
	Used          map[string]string `json:"used"`
}

type PVC struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	Phase        string `json:"phase"`
	Capacity     int64  `json:"capacity"`
	StorageClass string `json:"storage_class"`
	WorkspaceID  string `json:"workspace_id"`
}

type Container struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Kind          Kind      `json:"kind"`
	State         string    `json:"state"`
	Status        string    `json:"status"`
	Image         string    `json:"image"`
	WorkspaceID   string    `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name"`
	TaskID        string    `json:"task_id"`
	TaskTitle     string    `json:"task_title,omitempty"`   // Set by the API only on containers in the reader's own workspace.
	ProjectName   string    `json:"project_name,omitempty"` // Set by the API only on the reader's own prewarm sandboxes.
	Agent         string    `json:"agent"`
	Stage         string    `json:"stage"`
	Service       string    `json:"service"`
	StartedAt     time.Time `json:"started_at,omitzero"`
	Instance      string    `json:"instance,omitempty"`
	// Set by the API (orphan_agents.go), never by the collector.
	Orphan bool `json:"orphan,omitempty"`

	CPUCores float64 `json:"cpu_cores"`
	HasUsage bool    `json:"has_usage"`
	MemBytes uint64  `json:"mem_bytes"`
	MemLimit uint64  `json:"mem_limit"`
	NetRx    uint64  `json:"net_rx"`
	NetTx    uint64  `json:"net_tx"`
	BlkRead  uint64  `json:"blk_read"`
	BlkWrite uint64  `json:"blk_write"`
	Pids     uint64  `json:"pids"`

	Namespace       string `json:"namespace,omitempty"`
	Node            string `json:"node,omitempty"`
	RuntimeClass    string `json:"runtime_class,omitempty"`
	CPURequestMilli int64  `json:"cpu_request_milli,omitempty"`
	CPULimitMilli   int64  `json:"cpu_limit_milli,omitempty"`
	MemRequest      int64  `json:"mem_request,omitempty"`
	Restarts        int32  `json:"restarts,omitempty"`
	running         bool
}

func (c *Container) IsRunning() bool { return c.running }

type ContainerTotals struct {
	Total   int `json:"total"`
	Running int `json:"running"`
}

type KindUsage struct {
	Kind     Kind    `json:"kind"`
	Count    int     `json:"count"`
	Running  int     `json:"running"`
	CPUCores float64 `json:"cpu_cores"`
	MemBytes uint64  `json:"mem_bytes"`
}

type Octopus struct {
	ActiveRuns      []ActiveRun     `json:"active_runs"`
	ActiveRunsTotal int             `json:"active_runs_total"`
	Queue           Admission       `json:"queue"`
	Limits          []Limit         `json:"limits"`
	Counts          Counts          `json:"counts"`
	Postgres        *Postgres       `json:"postgres,omitempty"`
	Workspaces      []WorkspaceLoad `json:"workspaces"`
}

type ActiveRun struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	Agent         string `json:"agent"`
	Stage         string `json:"stage"`
	Count         int    `json:"count"`
}

type Admission struct {
	Agent   Gate `json:"agent"`
	Mention Gate `json:"mention"`
	Room    int  `json:"room"`
}

// Cap 0 is unlimited.
type Gate struct {
	Cap      int `json:"cap"`
	Inflight int `json:"inflight"`
	Waiting  int `json:"waiting"`
}

// Cap 0 is unlimited.
type Limit struct {
	Key   string `json:"key"`
	Cap   int    `json:"cap"`
	InUse int    `json:"in_use"`
}

type Counts struct {
	Workspaces int `json:"workspaces"`
	Users      int `json:"users"`
	Tasks      int `json:"tasks"`
	Projects   int `json:"projects"`
}

type Postgres struct {
	DatabaseSize   int64       `json:"database_size"`
	Connections    int         `json:"connections"`
	MaxConnections int         `json:"max_connections"`
	Version        string      `json:"version"`
	LargestTables  []TableSize `json:"largest_tables"`
}

type TableSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Rows  int64  `json:"rows"`
}

type WorkspaceLoad struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Plan              string  `json:"plan"`
	ConcurrencyLimit  int     `json:"concurrency_limit"`
	RunningContainers int     `json:"running_containers"`
	RunningAgents     int     `json:"running_agents"`
	CPUCores          float64 `json:"cpu_cores"`
	MemBytes          uint64  `json:"mem_bytes"`
	RunsToday         int     `json:"runs_today"`
	TokensToday       int64   `json:"tokens_today"`
	CostToday         float64 `json:"cost_today"`
	StorageBytes      int64   `json:"storage_bytes"`
}

type Storage struct {
	DataDir    string    `json:"data_dir"`
	ComputedAt time.Time `json:"computed_at,omitzero"`
	Total      int64     `json:"total"`
	Dirs       []DirSize `json:"dirs"`
	Partial    bool      `json:"partial"`
}

type DirSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type Point struct {
	T           time.Time `json:"t"`
	CPUPercent  float64   `json:"cpu"`
	MemPercent  float64   `json:"mem"`
	DiskPercent float64   `json:"disk"`
	RxBps       float64   `json:"rx"`
	TxBps       float64   `json:"tx"`
	Running     int       `json:"running"`
	ActiveRuns  int       `json:"runs"`
}
