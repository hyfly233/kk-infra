package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RealKubeClient 真实 Kubernetes 客户端。
// 直接调用 K8s REST API（不引入 client-go 重依赖），
// 通过 kubeconfig 认证。无 GPU 节点时可用虚拟 GPU 池（配置注入）做资源校验。
type RealKubeClient struct {
	baseURL    string // https://<apiserver>
	httpClient *http.Client
	namespace  string // 默认命名空间（部署请求未指定时）
	token      string // bearer token（kubeconfig）
	// 虚拟 GPU 池配置（Docker Desktop 等无 GPU 环境使用）
	virtualGPUs []FakeNodeConfig
	// 部署镜像（vLLM 或 mock；由环境变量注入便于无 GPU 验证）
	deployImage        string
	kedaEnabled        bool
	prometheusURL      string
	artifactEndpoint   string
	artifactAccessKey  string
	artifactSecretKey  string
	artifactSecure     bool
	progressiveEnabled bool
}

// ConfigureKEDA controls dynamic ScaledObject reconciliation for real clusters.
func (c *RealKubeClient) ConfigureKEDA(enabled bool, prometheusURL string) {
	c.kedaEnabled = enabled
	c.prometheusURL = strings.TrimRight(prometheusURL, "/")
}

func (c *RealKubeClient) ConfigureArtifactStorage(endpoint, accessKey, secretKey string, secure bool) {
	c.artifactEndpoint = endpoint
	c.artifactAccessKey = accessKey
	c.artifactSecretKey = secretKey
	c.artifactSecure = secure
}

func (c *RealKubeClient) ConfigureProgressiveDelivery(enabled bool) { c.progressiveEnabled = enabled }

// KubeConfig kubeconfig 结构（子集）
type KubeConfig struct {
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server                   string `yaml:"server"`
			CertificateAuthorityData string `yaml:"certificate-authority-data"`
			InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster string `yaml:"cluster"`
			User    string `yaml:"user"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			Token                 string `yaml:"token"`
			ClientCertificateData string `yaml:"client-certificate-data"`
			ClientKeyData         string `yaml:"client-key-data"`
		} `yaml:"user"`
	} `yaml:"users"`
}

// NewRealKubeClient 从 kubeconfig 创建真实客户端。
// kubeconfigPath 为空时使用 ~/.kube/config。
// virtualGPUConfig: 虚拟 GPU 池（env 注入，格式 nodeName:gpuType:count:memMB:util:health）
// deployImage: 部署使用的镜像（默认 vllm/vllm-openai:latest）
func NewRealKubeClient(kubeconfigPath, virtualGPUConfig, deployImage string) (*RealKubeClient, error) {
	if kubeconfigPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("获取 home 目录失败: %w", err)
		}
		kubeconfigPath = filepath.Join(home, ".kube", "config")
	}
	data, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("读取 kubeconfig %s 失败: %w", kubeconfigPath, err)
	}
	var kc KubeConfig
	if err := yaml.Unmarshal(data, &kc); err != nil {
		return nil, fmt.Errorf("解析 kubeconfig 失败: %w", err)
	}

	// 定位当前 context 的 cluster/user
	var server, caData, token, certData, keyData string
	insecure := false
	for _, c := range kc.Contexts {
		if c.Name == kc.CurrentContext {
			for _, cl := range kc.Clusters {
				if cl.Name == c.Context.Cluster {
					server = cl.Cluster.Server
					caData = cl.Cluster.CertificateAuthorityData
					insecure = cl.Cluster.InsecureSkipTLSVerify
				}
			}
			for _, u := range kc.Users {
				if u.Name == c.Context.User {
					token = u.User.Token
					certData = u.User.ClientCertificateData
					keyData = u.User.ClientKeyData
				}
			}
		}
	}
	if server == "" {
		return nil, fmt.Errorf("kubeconfig 中找不到当前 context: %s", kc.CurrentContext)
	}

	// TLS 配置
	tlsCfg := &tls.Config{InsecureSkipVerify: insecure} //nolint:gosec // kubeconfig 显式配置
	if caData != "" {
		if ca, err := base64.StdEncoding.DecodeString(caData); err == nil {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(ca)
			tlsCfg.RootCAs = pool
			tlsCfg.InsecureSkipVerify = false
		}
	}
	if certData != "" && keyData != "" {
		certPEM, err1 := base64.StdEncoding.DecodeString(certData)
		keyPEM, err2 := base64.StdEncoding.DecodeString(keyData)
		if err1 == nil && err2 == nil {
			if cert, err := tls.X509KeyPair(certPEM, keyPEM); err == nil {
				tlsCfg.Certificates = []tls.Certificate{cert}
			}
		}
	}

	return &RealKubeClient{
		baseURL:     server,
		httpClient:  &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}, Timeout: 30 * time.Second},
		namespace:   "default",
		token:       token,
		virtualGPUs: parseVirtualGPUConfig(virtualGPUConfig),
		deployImage: deployImage,
	}, nil
}

// parseVirtualGPUConfig 解析虚拟 GPU 配置（分号分隔）
// 格式: node:gpuType:count:memMB:util:health;node2:...
func parseVirtualGPUConfig(cfg string) []FakeNodeConfig {
	if cfg == "" {
		return nil
	}
	var out []FakeNodeConfig
	for _, part := range strings.Split(cfg, ";") {
		f := strings.Split(part, ":")
		if len(f) < 3 {
			continue
		}
		count, _ := strconv.ParseInt(f[2], 10, 32)
		mem, _ := strconv.ParseInt(f[3], 10, 64)
		util, _ := strconv.ParseFloat(f[4], 64)
		health := "Healthy"
		if len(f) > 5 {
			health = f[5]
		}
		out = append(out, FakeNodeConfig{
			NodeName:    f[0],
			GPUType:     f[1],
			GPUCount:    int32(count),
			MemoryMB:    mem,
			Utilization: util,
			Health:      health,
		})
	}
	return out
}

// ---- HTTP 封装 ----

type apiResponse struct {
	Kind string `json:"kind"`
}

func (c *RealKubeClient) do(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return fmt.Errorf("序列化请求失败: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, &buf)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	// PATCH 请求需要正确的 Content-Type（K8s 校验 media type）
	if method == "PATCH" {
		req.Header.Set("Content-Type", "application/merge-patch+json")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	// 认证：bearer token（kubeconfig 中）
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("调用 Kubernetes API 失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode >= 400 {
		// 404 视为 NotFound
		if resp.StatusCode == http.StatusNotFound {
			return ErrNotFound
		}
		return fmt.Errorf("Kubernetes API %s %s 返回 %d: %s", method, path, resp.StatusCode, truncateStr(string(data), 300))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("解析响应失败: %w", err)
		}
	}
	return nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
