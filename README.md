# bamboo-prometheus-exporter

Bamboo-prometheus-exporter is a tool that scrapes metrics from Atlassian Bamboo and exposes them for Prometheus monitoring, enabling detailed CI/CD pipeline observability.

## Features
- **Agent Monitoring** 
  Track agent status (enabled/active/busy) with labels including ID, name, and type  
- **Build Queue Analytics**   
  Monitor queue size changes and agent utilization rates in real-time
- **Project Build Metrics** 
  Capture success/failure counts. Capture total builds per project-plan combination
- **Dynamic Labeling**  
  Auto-split plan.name into project and name labels (e.g., XXXX Releases - PB-XXXX-21.2 → project="XXXX Releases", name="PB-XXXX-21.2")

## Installation

### Binary
```bash
go install github.com/EIETS/bamboo_exporter@latest
./bamboo_exporter \
  --bamboo.uri="http://bamboo.example.com" \
  --web.listen-address=":9117"
```

### Docker
```bash
docker run -d -p 9117:9117 -v ./config.json:/app/config.json eitets/bamboo_exporter \
  --bamboo.uri="https://bamboo.example.com" \
  --insecure
```

## Configuration
### Flags
| Parameter              | Default                 | Description              |
|:-----------------------|:------------------------|:-------------------------|
| --telemetry.endpoint   | /metrics                | Metrics exposure path    |
| --bamboo.uri           | http://localhost:8085   | Bamboo server URL        |
| --insecure             | false                   | Disable SSL verification |
| --web.listen-address   | :9117                   | Exporter listening port  |

### Authentication
Create config.json:
```json
{
  "bamboo_username": "admin",
  "bamboo_password": "secure_password"
}
```

### Exported Metrics
| Metric Name                  | Type    | Labels                                | Description                           |
|:-----------------------------|:--------|:--------------------------------------|:--------------------------------------|
| bamboo_agents_status         | Gauge   | id, name, type, enabled, active, busy | Agent operational states              |
| bamboo_queue_size            | Gauge   | -                                     | Current build queue count             |
| bamboo_queue_change          | Gauge   | -                                     | Queue size delta since last scrape    |
| bamboo_agent_utilization     | Gauge   | -                                     | Busy/active agent ratio               |
| bamboo_build_success_total   | Counter | -                                     | Total successful builds               |
| bamboo_build_failure_total   | Counter | -                                     | Total failed builds                   |
| bamboo_build_total           | Gauge   | project, name                         | Latest build number per plan          |

## FAQ
Q: Metrics not appearing?

A.Verify Bamboo API accessibility: curl -u user:pass http://bamboo-server/rest/api/latest/queue

Check label parsing rules in parseProjectAndName()

Q: Authentication failures?

A.Ensure config.json has 600 permissions

Confirm user has ​​View Configuration​​ permissions in Bamboo

Q: Handle large-scale deployments?

A.Deploy multiple exporters with Prometheus service discovery


