// Package k8s 最小化 Kubernetes 资源结构体。
// MVP 不引入 client-go，自写核心字段；真实集群适配时映射到 client-go 类型。
package k8s

// ObjectMeta 元信息
type ObjectMeta struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Container 容器定义
type Container struct {
	Name           string               `json:"name"`
	Image          string               `json:"image"`
	Command        []string             `json:"command,omitempty"`
	Args           []string             `json:"args,omitempty"`
	Env            []EnvVar             `json:"env,omitempty"`
	Resources      ResourceRequirements `json:"resources"`
	Ports          []ContainerPort      `json:"ports,omitempty"`
	ReadinessProbe *Probe               `json:"readinessProbe,omitempty"`
	LivenessProbe  *Probe               `json:"livenessProbe,omitempty"`
	VolumeMounts   []VolumeMount        `json:"volumeMounts,omitempty"`
}

// EnvVar 环境变量
type EnvVar struct {
	Name      string        `json:"name"`
	Value     string        `json:"value,omitempty"`
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

type EnvVarSource struct {
	SecretKeyRef *SecretKeySelector `json:"secretKeyRef,omitempty"`
}

type SecretKeySelector struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type VolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
}

type Volume struct {
	Name     string          `json:"name"`
	EmptyDir *EmptyDirVolume `json:"emptyDir,omitempty"`
}

type EmptyDirVolume struct{}

// ContainerPort 容器端口
type ContainerPort struct {
	Name          string `json:"name,omitempty"`
	ContainerPort int32  `json:"containerPort"`
}

// ResourceRequirements 资源需求
type ResourceRequirements struct {
	Limits   map[string]string `json:"limits,omitempty"`
	Requests map[string]string `json:"requests,omitempty"`
}

// Probe 探针
type Probe struct {
	HTTPGet             *HTTPGetAction `json:"httpGet,omitempty"`
	InitialDelaySeconds int32          `json:"initialDelaySeconds,omitempty"`
	PeriodSeconds       int32          `json:"periodSeconds,omitempty"`
}

// HTTPGetAction HTTP 探针动作
type HTTPGetAction struct {
	Path string `json:"path"`
	Port int32  `json:"port"`
}

// PodTemplateSpec Pod 模板
type PodTemplateSpec struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PodSpec    `json:"spec"`
}

// PodSpec Pod 规格
type PodSpec struct {
	NodeSelector   map[string]string `json:"nodeSelector,omitempty"`
	Containers     []Container       `json:"containers"`
	RestartPolicy  string            `json:"restartPolicy,omitempty"`
	InitContainers []Container       `json:"initContainers,omitempty"`
	Volumes        []Volume          `json:"volumes,omitempty"`
}

// Deployment Kubernetes Deployment
type Deployment struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   ObjectMeta     `json:"metadata"`
	Spec       DeploymentSpec `json:"spec"`
}

// LabelSelector K8s 标签选择器（spec.selector 需要 matchLabels 嵌套）
type LabelSelector struct {
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// DeploymentSpec Deployment 规格
type DeploymentSpec struct {
	Replicas int32           `json:"replicas"`
	Selector *LabelSelector  `json:"selector"`
	Template PodTemplateSpec `json:"template"`
}

// Service Kubernetes Service
type Service struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Metadata   ObjectMeta  `json:"metadata"`
	Spec       ServiceSpec `json:"spec"`
}

// ServiceSpec Service 规格
type ServiceSpec struct {
	Selector map[string]string `json:"selector"`
	Ports    []ServicePort     `json:"ports"`
	Type     string            `json:"type"`
}

// ServicePort 服务端口
type ServicePort struct {
	Name       string `json:"name,omitempty"`
	Port       int32  `json:"port"`
	TargetPort int32  `json:"targetPort"`
}

// Secret Kubernetes Secret（数据 base64，MVP 存明文标记）
type Secret struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   ObjectMeta        `json:"metadata"`
	Type       string            `json:"type"`
	StringData map[string]string `json:"stringData,omitempty"`
}

// ConfigMap Kubernetes ConfigMap
type ConfigMap struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   ObjectMeta        `json:"metadata"`
	Data       map[string]string `json:"data,omitempty"`
}

// Pod Kubernetes Pod 摘要（状态同步用）
type Pod struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels,omitempty"`
	Phase     string            `json:"phase"` // Pending/Running/Succeeded/Failed/Unknown
	Ready     bool              `json:"ready"`
	Reason    string            `json:"reason,omitempty"`
	Message   string            `json:"message,omitempty"`
}

// Event Kubernetes 事件
type Event struct {
	Type     string `json:"type"` // Normal/Warning
	Reason   string `json:"reason"`
	Message  string `json:"message"`
	LastTime string `json:"lastTime,omitempty"`
}

// DeploymentStatus 同步的 Deployment 状态
type DeploymentStatus struct {
	Replicas          int32  `json:"replicas"`
	ReadyReplicas     int32  `json:"readyReplicas"`
	AvailableReplicas int32  `json:"availableReplicas"`
	Condition         string `json:"condition"` // Available/Progressing/ReplicaFailure/Unknown
	Message           string `json:"message,omitempty"`
}
