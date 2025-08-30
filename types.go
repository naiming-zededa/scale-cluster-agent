package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Config holds runtime configuration for the agent.
type Config struct {
    RancherURL  string `json:"RancherURL"`
    BearerToken string `json:"BearerToken"`
    ListenPort  int    `json:"ListenPort"`
    LogLevel    string `json:"LogLevel"`
    // Multi-tenant KWOK mode: one main KWOK cluster, each logical cluster as a namespace/proxy
    MultiTenant     bool   `json:"MultiTenant"`
    MainClusterName string `json:"MainClusterName"`
    MainAPIPort     int    `json:"MainAPIPort"`   // secure or insecure port exposed by KWOK apiserver
    ProxyBasePort   int    `json:"ProxyBasePort"` // starting port for per-virtual-cluster proxies

    // Diagnostics/profiling
    PprofEnable        bool `json:"PprofEnable"`
    PprofPort          int  `json:"PprofPort"`
    MemLogIntervalSec  int  `json:"MemLogIntervalSec"` // if >0, periodically logs mem stats
}

// ScaleAgent is the main application state container.
type ScaleAgent struct {
    config  *Config
    ctx     context.Context
    cancel  context.CancelFunc

    // Cluster data and KWOK manager
    clusters    map[string]*ClusterInfo
    kwokManager *KWOKClusterManager

    // HTTP server
    httpServer *http.Server
    httpServerOnce sync.Once

    // Connection/session tracking
    activeConnections     map[string]bool
    clusterAgentSessions  map[string]bool
    firstClusterConnected bool
    // Optional cancel funcs per cluster for active tunnels
    connectCancels        map[string]context.CancelFunc

    // Caches and helpers
    tokenCache     map[string]string
    mockServers    map[string]*http.Server
    mockCertPEM    []byte
    portForwarders map[string]*PortForwarder
    nextPort       int
    nameCounters   map[string]int
    connecting     map[string]bool

    // Multi-tenant proxy management
    proxyPorts     map[string]int       // clusterID -> local proxy port
    proxyCmds      map[string]*exec.Cmd // clusterID -> running kubectl proxy command
    nextProxyPort  int

    // Synchronization
    connMutex sync.RWMutex
    caMutex   sync.RWMutex

    // Backoff / attempt tracking
    lastConnectAttempt   map[string]time.Time
    lastCAConnectAttempt map[string]time.Time
}

// PortForwarder is a placeholder type for future port-forward management.
type PortForwarder struct{}

// CreateClusterRequest represents the payload to create a cluster via API.
type CreateClusterRequest struct {
    Name string `json:"name"`
}

// CreateClusterResponse represents the response after creating a cluster.
type CreateClusterResponse struct {
    Success   bool   `json:"success"`
    Message   string `json:"message"`
    ClusterID string `json:"clusterID"`
}

// ClusterInfo models a simulated cluster's high-level state.
type ClusterInfo struct {
    Name        string           `json:"name" yaml:"name"`
    ClusterID   string           `json:"clusterID,omitempty" yaml:"clusterID,omitempty"`
    Status      string           `json:"status,omitempty" yaml:"status,omitempty"`
    Nodes       []NodeInfo       `json:"nodes,omitempty" yaml:"nodes,omitempty"`
    Pods        []PodInfo        `json:"pods,omitempty" yaml:"pods,omitempty"`
    Services    []ServiceInfo    `json:"services,omitempty" yaml:"services,omitempty"`
    Secrets     []SecretInfo     `json:"secrets,omitempty" yaml:"secrets,omitempty"`
    ConfigMaps  []ConfigMapInfo  `json:"configmaps,omitempty" yaml:"configmaps,omitempty"`
    Deployments []DeploymentInfo `json:"deployments,omitempty" yaml:"deployments,omitempty"`
}

// NodeInfo summarizes a node for reporting.
type NodeInfo struct {
    Name             string            `json:"name" yaml:"name"`
    Status           string            `json:"status" yaml:"status"`
    Roles            []string          `json:"roles,omitempty" yaml:"roles,omitempty"`
    Age              string            `json:"age,omitempty" yaml:"age,omitempty"`
    Version          string            `json:"version,omitempty" yaml:"version,omitempty"`
    InternalIP       string            `json:"internalIP,omitempty" yaml:"internalIP,omitempty"`
    ExternalIP       string            `json:"externalIP,omitempty" yaml:"externalIP,omitempty"`
    OSImage          string            `json:"osImage,omitempty" yaml:"osImage,omitempty"`
    KernelVer        string            `json:"kernelVer,omitempty" yaml:"kernelVer,omitempty"`
    ContainerRuntime string            `json:"containerRuntime,omitempty" yaml:"containerRuntime,omitempty"`
    Capacity         map[string]string `json:"capacity,omitempty" yaml:"capacity,omitempty"`
    Allocatable      map[string]string `json:"allocatable,omitempty" yaml:"allocatable,omitempty"`
}

// PodInfo summarizes a pod for reporting.
type PodInfo struct {
    Name      string            `json:"name" yaml:"name"`
    Namespace string            `json:"namespace" yaml:"namespace"`
    Status    string            `json:"status" yaml:"status"`
    Ready     string            `json:"ready,omitempty" yaml:"ready,omitempty"`
    Restarts  int               `json:"restarts,omitempty" yaml:"restarts,omitempty"`
    Age       string            `json:"age,omitempty" yaml:"age,omitempty"`
    IP        string            `json:"ip,omitempty" yaml:"ip,omitempty"`
    Node      string            `json:"node,omitempty" yaml:"node,omitempty"`
    Labels    map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

// ServiceInfo summarizes a service for reporting.
type ServiceInfo struct {
    Name       string            `json:"name" yaml:"name"`
    Namespace  string            `json:"namespace" yaml:"namespace"`
    Type       string            `json:"type,omitempty" yaml:"type,omitempty"`
    ClusterIP  string            `json:"clusterIP,omitempty" yaml:"clusterIP,omitempty"`
    ExternalIP string            `json:"externalIP,omitempty" yaml:"externalIP,omitempty"`
    Ports      string            `json:"ports,omitempty" yaml:"ports,omitempty"`
    Age        string            `json:"age,omitempty" yaml:"age,omitempty"`
    Labels     map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

// SecretInfo summarizes a secret for reporting (data keys only).
type SecretInfo struct {
    Name      string   `json:"name" yaml:"name"`
    Namespace string   `json:"namespace" yaml:"namespace"`
    Type      string   `json:"type,omitempty" yaml:"type,omitempty"`
    Data      int      `json:"data,omitempty" yaml:"data,omitempty"`
    DataKeys  []string `json:"dataKeys,omitempty" yaml:"dataKeys,omitempty"`
    Age       string   `json:"age,omitempty" yaml:"age,omitempty"`
}

// ConfigMapInfo summarizes a configmap for reporting (keys only).
type ConfigMapInfo struct {
    Name      string   `json:"name" yaml:"name"`
    Namespace string   `json:"namespace" yaml:"namespace"`
    Data      int      `json:"data,omitempty" yaml:"data,omitempty"`
    DataKeys  []string `json:"dataKeys,omitempty" yaml:"dataKeys,omitempty"`
    Age       string   `json:"age,omitempty" yaml:"age,omitempty"`
}

// DeploymentInfo summarizes a deployment for reporting.
type DeploymentInfo struct {
    Name       string            `json:"name" yaml:"name"`
    Namespace  string            `json:"namespace" yaml:"namespace"`
    Ready      string            `json:"ready,omitempty" yaml:"ready,omitempty"`
    UpToDate   string            `json:"upToDate,omitempty" yaml:"upToDate,omitempty"`
    Available  string            `json:"available,omitempty" yaml:"available,omitempty"`
    Age        string            `json:"age,omitempty" yaml:"age,omitempty"`
    Labels     map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

// stateFile returns the path to the persisted state file under the user's home.
func stateFile() (string, error) {
    home, err := os.UserHomeDir()
    if err != nil {
        return "", fmt.Errorf("failed to get home directory: %w", err)
    }
    dir := filepath.Join(home, ".scale-cluster-agent")
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return "", fmt.Errorf("failed to create state dir: %w", err)
    }
    return filepath.Join(dir, "state.json"), nil
}

// SaveState persists minimal agent state to disk (best-effort, non-fatal).
func (a *ScaleAgent) SaveState() error {
    if a == nil {
        return nil
    }
    path, err := stateFile()
    if err != nil {
        return err
    }
    // Only persist clusters; KWOK runtime state is reconstructed on demand.
    payload := struct {
        Clusters   map[string]*ClusterInfo `json:"clusters"`
        ProxyPorts map[string]int          `json:"proxyPorts,omitempty"`
        Version    string                  `json:"version"`
    }{
        Clusters:   a.clusters,
        ProxyPorts: a.proxyPorts,
        Version:    version,
    }
    data, err := json.MarshalIndent(payload, "", "  ")
    if err != nil {
        return fmt.Errorf("failed to marshal state: %w", err)
    }
    dir := filepath.Dir(path)
    // Write atomically: write to temp file, fsync, then rename over
    tmp, err := os.CreateTemp(dir, "state-*.json.tmp")
    if err != nil {
        return fmt.Errorf("failed to create temp state file: %w", err)
    }
    tmpName := tmp.Name()
    // Ensure temp file is removed on failure paths
    defer func() { _ = os.Remove(tmpName) }()

    if _, err := tmp.Write(data); err != nil {
        _ = tmp.Close()
        return fmt.Errorf("failed to write temp state file: %w", err)
    }
    if err := tmp.Sync(); err != nil {
        _ = tmp.Close()
        return fmt.Errorf("failed to sync temp state file: %w", err)
    }
    if err := tmp.Close(); err != nil {
        return fmt.Errorf("failed to close temp state file: %w", err)
    }

    // Best-effort backup of current state before replacing
    if _, statErr := os.Stat(path); statErr == nil {
        if b, rErr := os.ReadFile(path); rErr == nil && len(b) > 0 {
            _ = os.WriteFile(path+".bak", b, 0o644)
        }
    }

    if err := os.Rename(tmpName, path); err != nil {
        return fmt.Errorf("failed to replace state file: %w", err)
    }
    // fsync directory to persist rename on crash-prone systems (best-effort)
    if d, derr := os.Open(dir); derr == nil {
        _ = d.Sync()
        _ = d.Close()
    }
    return nil
}

// LoadState attempts to restore minimal agent state from disk.
func (a *ScaleAgent) LoadState() error {
    if a == nil {
        return nil
    }
    path, err := stateFile()
    if err != nil {
        return err
    }
    b, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            // Initialize empty state on first run
            if a.clusters == nil {
                a.clusters = make(map[string]*ClusterInfo)
            }
            return nil
        }
        return fmt.Errorf("failed to read state: %w", err)
    }
    // If file is empty or corrupt, try backup
    use := b
    var payload struct {
        Clusters   map[string]*ClusterInfo `json:"clusters"`
        ProxyPorts map[string]int          `json:"proxyPorts,omitempty"`
        Version    string                  `json:"version"`
    }
    if len(use) == 0 || json.Unmarshal(use, &payload) != nil {
        if bb, berr := os.ReadFile(path+".bak"); berr == nil && len(bb) > 0 {
            if uerr := json.Unmarshal(bb, &payload); uerr == nil {
                // Restore from backup
                use = bb
            } else {
                return fmt.Errorf("failed to unmarshal state and backup: %v / %v", json.Unmarshal(use, &payload), uerr)
            }
        } else {
            return fmt.Errorf("failed to unmarshal state: %w", json.Unmarshal(use, &payload))
        }
    }
    if payload.Clusters == nil {
        payload.Clusters = make(map[string]*ClusterInfo)
    }
    a.clusters = payload.Clusters
    if payload.ProxyPorts == nil { payload.ProxyPorts = make(map[string]int) }
    a.proxyPorts = payload.ProxyPorts
    return nil
}
