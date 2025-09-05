package main

import (
    "encoding/json"
    "fmt"
    "os/exec"
    "path/filepath"
    "strings"
    "time"
    "github.com/sirupsen/logrus"
)

// startPodUsageAnnotator periodically counts non-terminated pods per node in the main KWOK cluster
// and annotates each Node with Rancher's expected pod requests annotation so the UI shows used pod counts.
// Annotation keys mimicking Rancher controller: management.cattle.io/pod-requests (JSON map including pods).
func startPodUsageAnnotator(a *ScaleAgent, interval time.Duration) {
    if !a.config.MultiTenant { return } // only meaningful in shared main cluster mode
    if interval <= 0 { interval = 30 * time.Second }
    kubeconfig := filepath.Join(osUserHome(), ".kwok", "clusters", a.config.MainClusterName, "kubeconfig.yaml")
    go func(){
        // Do one immediate pass so annotations appear quickly
        if err := annotateNodePodUsage(kubeconfig); err != nil {
            logrus.Infof("podUsageAnnotator initial pass: %v", err)
        }
        ticker := time.NewTicker(interval)
        defer ticker.Stop()
        for range ticker.C {
            if err := annotateNodePodUsage(kubeconfig); err != nil {
                logrus.Debugf("podUsageAnnotator: %v", err)
            }
        }
    }()
}

func annotateNodePodUsage(kubeconfig string) error {
    // Get full pod list JSON and count non-terminal pods by node.
    getPods := exec.Command("kubectl", "--kubeconfig", kubeconfig, "get", "pods", "-A", "-o", "json")
    podJSON, err := getPods.CombinedOutput()
    counts := map[string]int{}
    if err == nil {
        var podList struct {
            Items []struct {
                Spec struct {
                    NodeName string `json:"nodeName"`
                } `json:"spec"`
                Status struct {
                    Phase string `json:"phase"`
                } `json:"status"`
            } `json:"items"`
        }
        if perr := json.Unmarshal(podJSON, &podList); perr == nil {
            for _, p := range podList.Items {
                if p.Spec.NodeName == "" { continue }
                if p.Status.Phase == "Succeeded" || p.Status.Phase == "Failed" { continue }
                counts[p.Spec.NodeName]++
            }
        } else {
            logrus.Debugf("podUsageAnnotator: unmarshal pods failed: %v", perr)
        }
    } else {
        logrus.Debugf("podUsageAnnotator: kubectl get pods failed: %v (%s)", err, string(podJSON))
    }

    // Get all node names so we also annotate zero counts
    getNodes := exec.Command("kubectl", "--kubeconfig", kubeconfig, "get", "nodes", "-o", "jsonpath={range .items[*]}{.metadata.name}{'\\n'}{end}")
    nodeOut, err := getNodes.CombinedOutput()
    if err != nil { return fmt.Errorf("kubectl get nodes: %v (%s)", err, string(nodeOut)) }
    nodeLines := []string{}
    if trimmed := strings.TrimSpace(string(nodeOut)); trimmed != "" { nodeLines = strings.Split(trimmed, "\n") }
    if len(nodeLines) == 0 { return nil }

    for _, node := range nodeLines {
        if node == "" { continue }
        c := counts[node]
        // Rancher expects the annotation value itself to be a JSON string containing a JSON object.
        inner := fmt.Sprintf(`{\"pods\":\"%d\"}`, c)
        patch := fmt.Sprintf(`{"metadata":{"annotations":{"management.cattle.io/pod-requests":"%s"}}}`, inner)
        cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "patch", "node", node, "--type=merge", "-p", patch)
        if out, err := cmd.CombinedOutput(); err != nil {
            logrus.Infof("podUsageAnnotator: patch node %s pods=%d failed: %v (%s)", node, c, err, string(out))
        } else {
            logrus.Debugf("podUsageAnnotator: node %s annotated pods=%d", node, c)
        }
    }
    return nil
}

func osUserHome() string { h, _ := exec.Command("bash", "-c", "echo $HOME").Output(); return strings.TrimSpace(string(h)) }
