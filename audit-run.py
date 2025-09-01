#!/usr/bin/env python3
"""
Kubernetes Audit Log Analyzer for Rancher Multi-Cluster Setup
Analyzes audit logs to understand API request patterns from Rancher clusters
"""

import json
import sys
from collections import defaultdict, Counter
from datetime import datetime
import re
from urllib.parse import urlparse, parse_qs

class RancherAuditAnalyzer:
    def __init__(self):
        self.clusters = defaultdict(lambda: {
            'requests': [],
            'endpoints': Counter(),
            'verbs': Counter(),
            'resources': Counter(),
            'watch_streams': [],
            'namespaces_accessed': set(),
            'first_seen': None,
            'last_seen': None
        })
        self.system_requests = []
        self.total_requests = 0
        
    def parse_log_line(self, line):
        """Parse a single audit log line"""
        try:
            return json.loads(line.strip())
        except json.JSONDecodeError:
            return None
            
    def extract_cluster_id(self, user_info):
        """Extract Rancher cluster ID from user information"""
        username = user_info.get('username', '')
        
        # Pattern: system:serviceaccount:cattle-system-{cluster_id}:cattle-{cluster_id}
        cattle_match = re.search(r'cattle-system-([^:]+):cattle-([^:]+)', username)
        if cattle_match:
            return f"rancher-{cattle_match.group(1)}"
        
        # System users (kube-scheduler, kube-controller-manager, etc.)
        if username in ['kwok-admin', 'system:apiserver'] or username.startswith('system:'):
            return 'kwok-system'
            
        return 'unknown'
    
    def analyze_request_uri(self, uri):
        """Analyze request URI to extract resource information"""
        # Remove query parameters for analysis
        base_uri = uri.split('?')[0]
        
        # Parse API path
        parts = base_uri.strip('/').split('/')
        
        resource_info = {
            'api_group': 'core',
            'version': 'v1',
            'resource': 'unknown',
            'namespaced': False,
            'namespace': None
        }
        
        if len(parts) >= 2:
            if parts[0] == 'api':
                # Core API: /api/v1/...
                resource_info['api_group'] = 'core'
                if len(parts) > 1:
                    resource_info['version'] = parts[1]
                if len(parts) > 2:
                    if parts[2] == 'namespaces' and len(parts) > 3:
                        resource_info['namespace'] = parts[3]
                        resource_info['namespaced'] = True
                        if len(parts) > 4:
                            resource_info['resource'] = parts[4]
                        else:
                            resource_info['resource'] = 'namespaces'
                    else:
                        resource_info['resource'] = parts[2]
                        
            elif parts[0] == 'apis':
                # Extended APIs: /apis/group/version/...
                if len(parts) > 2:
                    resource_info['api_group'] = parts[1]
                    resource_info['version'] = parts[2]
                    if len(parts) > 3:
                        if parts[3] == 'namespaces' and len(parts) > 4:
                            resource_info['namespace'] = parts[4]
                            resource_info['namespaced'] = True
                            if len(parts) > 5:
                                resource_info['resource'] = parts[5]
                        else:
                            resource_info['resource'] = parts[3]
        
        # Parse query parameters
        query_params = {}
        if '?' in uri:
            try:
                query_params = parse_qs(uri.split('?')[1])
                # Flatten single-item lists
                query_params = {k: v[0] if len(v) == 1 else v for k, v in query_params.items()}
            except:
                pass
        
        resource_info['query_params'] = query_params
        return resource_info
    
    def is_watch_request(self, uri):
        """Check if this is a watch request"""
        return 'watch=true' in uri
        
    def analyze_entry(self, entry):
        """Analyze a single audit log entry"""
        if not entry:
            return
            
        self.total_requests += 1
        
        # Extract basic info
        timestamp = entry.get('requestReceivedTimestamp')
        user = entry.get('user', {})
        uri = entry.get('requestURI', '')
        verb = entry.get('verb', '')
        stage = entry.get('stage', '')
        
        # Only analyze completed requests
        if stage not in ['ResponseComplete', 'ResponseStarted']:
            return
            
        cluster_id = self.extract_cluster_id(user)
        resource_info = self.analyze_request_uri(uri)
        
        # Parse timestamp
        try:
            ts = datetime.fromisoformat(timestamp.replace('Z', '+00:00'))
        except:
            ts = None
            
        request_data = {
            'timestamp': timestamp,
            'parsed_timestamp': ts,
            'uri': uri,
            'verb': verb,
            'resource_info': resource_info,
            'user': user.get('username', ''),
            'user_agent': entry.get('userAgent', ''),
            'stage': stage
        }
        
        if cluster_id == 'kwok-system':
            self.system_requests.append(request_data)
        else:
            cluster_data = self.clusters[cluster_id]
            cluster_data['requests'].append(request_data)
            
            # Update counters
            cluster_data['endpoints'][uri.split('?')[0]] += 1
            cluster_data['verbs'][verb] += 1
            
            resource_key = f"{resource_info['api_group']}/{resource_info['resource']}"
            cluster_data['resources'][resource_key] += 1
            
            if resource_info['namespace']:
                cluster_data['namespaces_accessed'].add(resource_info['namespace'])
                
            # Track watch streams
            if self.is_watch_request(uri):
                cluster_data['watch_streams'].append(request_data)
                
            # Update timestamps
            if ts:
                if not cluster_data['first_seen'] or ts < cluster_data['first_seen']:
                    cluster_data['first_seen'] = ts
                if not cluster_data['last_seen'] or ts > cluster_data['last_seen']:
                    cluster_data['last_seen'] = ts
    
    def analyze_file(self, filename):
        """Analyze audit log file"""
        print(f"Analyzing audit log: {filename}")
        
        with open(filename, 'r') as f:
            for line_num, line in enumerate(f, 1):
                entry = self.parse_log_line(line)
                if entry:
                    self.analyze_entry(entry)
                    
                if line_num % 1000 == 0:
                    print(f"Processed {line_num} lines...")
        
        print(f"Analysis complete. Processed {self.total_requests} total requests.")
    
    def print_summary(self):
        """Print analysis summary"""
        print("\n" + "="*80)
        print("RANCHER MULTI-CLUSTER AUDIT LOG ANALYSIS")
        print("="*80)
        
        print(f"\nTotal Requests Analyzed: {self.total_requests}")
        print(f"Rancher Clusters Detected: {len(self.clusters)}")
        print(f"System Requests: {len(self.system_requests)}")
        
        # Cluster analysis
        for cluster_id, data in self.clusters.items():
            print(f"\n{'─'*60}")
            print(f"CLUSTER: {cluster_id}")
            print(f"{'─'*60}")
            
            print(f"Total Requests: {len(data['requests'])}")
            
            if data['first_seen'] and data['last_seen']:
                duration = data['last_seen'] - data['first_seen']
                print(f"Activity Period: {data['first_seen'].strftime('%Y-%m-%d %H:%M:%S')} → {data['last_seen'].strftime('%Y-%m-%d %H:%M:%S')}")
                print(f"Duration: {duration}")
                
                if duration.total_seconds() > 0:
                    req_per_minute = len(data['requests']) / (duration.total_seconds() / 60)
                    print(f"Request Rate: {req_per_minute:.2f} requests/minute")
            
            print(f"\nTop 10 API Endpoints:")
            for endpoint, count in data['endpoints'].most_common(10):
                print(f"  {count:4d} - {endpoint}")
            
            print(f"\nHTTP Verbs:")
            for verb, count in data['verbs'].most_common():
                print(f"  {verb:8s}: {count:4d}")
            
            print(f"\nTop Resources:")
            for resource, count in data['resources'].most_common(10):
                print(f"  {count:4d} - {resource}")
                
            print(f"\nNamespaces Accessed: {len(data['namespaces_accessed'])}")
            if data['namespaces_accessed']:
                ns_list = sorted(data['namespaces_accessed'])
                print(f"  {', '.join(ns_list)}")
            
            print(f"\nWatch Streams: {len(data['watch_streams'])}")
            if data['watch_streams']:
                print("  Active watch streams:")
                for watch in data['watch_streams'][-5:]:  # Show last 5
                    resource = watch['resource_info']['resource']
                    print(f"    - {watch['verb']} {resource} ({watch['timestamp']})")
        
        # System requests summary
        if self.system_requests:
            print(f"\n{'─'*60}")
            print(f"KWOK SYSTEM REQUESTS")
            print(f"{'─'*60}")
            
            system_endpoints = Counter()
            system_verbs = Counter()
            
            for req in self.system_requests:
                system_endpoints[req['uri'].split('?')[0]] += 1
                system_verbs[req['verb']] += 1
            
            print(f"Total System Requests: {len(self.system_requests)}")
            print(f"\nTop System Endpoints:")
            for endpoint, count in system_endpoints.most_common(10):
                print(f"  {count:4d} - {endpoint}")
    
    def detect_issues(self):
        """Detect potential issues with the setup"""
        print(f"\n{'─'*60}")
        print("ISSUE DETECTION")
        print(f"{'─'*60}")
        
        issues = []
        
        # Check if multiple clusters are accessing the same resources
        if len(self.clusters) > 1:
            # Check for overlapping namespaces
            all_namespaces = set()
            cluster_namespaces = {}
            
            for cluster_id, data in self.clusters.items():
                cluster_namespaces[cluster_id] = data['namespaces_accessed']
                all_namespaces.update(data['namespaces_accessed'])
            
            # Find overlapping namespaces
            for ns in all_namespaces:
                accessing_clusters = [cid for cid, nss in cluster_namespaces.items() if ns in nss]
                if len(accessing_clusters) > 1:
                    issues.append(f"Namespace '{ns}' accessed by multiple clusters: {', '.join(accessing_clusters)}")
            
            # Check if clusters see identical resources
            endpoint_sets = {}
            for cluster_id, data in self.clusters.items():
                endpoint_sets[cluster_id] = set(data['endpoints'].keys())
            
            cluster_ids = list(endpoint_sets.keys())
            for i, cluster1 in enumerate(cluster_ids):
                for cluster2 in cluster_ids[i+1:]:
                    overlap = endpoint_sets[cluster1] & endpoint_sets[cluster2]
                    if len(overlap) > 5:  # Significant overlap
                        overlap_pct = len(overlap) / len(endpoint_sets[cluster1] | endpoint_sets[cluster2]) * 100
                        issues.append(f"High endpoint overlap ({overlap_pct:.1f}%) between {cluster1} and {cluster2}")
        
        if issues:
            print("\n⚠️  POTENTIAL ISSUES DETECTED:")
            for i, issue in enumerate(issues, 1):
                print(f"  {i}. {issue}")
            
            print(f"\n💡 RECOMMENDATIONS:")
            print("  - Multiple Rancher clusters seeing identical resources indicates lack of isolation")
            print("  - Consider using separate KWOK instances for true cluster isolation")
            print("  - Or implement API filtering proxy to provide different views per cluster")
        else:
            print("\n✅ No obvious issues detected")

def main():
    if len(sys.argv) != 2:
        print("Usage: python3 audit_analyzer.py <audit_log_file>")
        print("Example: python3 audit_analyzer.py /var/log/kubernetes/audit.log")
        sys.exit(1)
    
    filename = sys.argv[1]
    analyzer = RancherAuditAnalyzer()
    
    try:
        analyzer.analyze_file(filename)
        analyzer.print_summary()
        analyzer.detect_issues()
        
    except FileNotFoundError:
        print(f"Error: File '{filename}' not found")
        sys.exit(1)
    except Exception as e:
        print(f"Error analyzing file: {e}")
        sys.exit(1)

if __name__ == "__main__":
    main()
