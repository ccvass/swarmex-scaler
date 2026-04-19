# Swarmex Scaler

Horizontal autoscaling for Docker Swarm services based on CPU and RAM metrics.

Part of [Swarmex](https://github.com/ccvass/swarmex) — enterprise-grade orchestration for Docker Swarm.

## What It Does

Monitors CPU and memory usage via Prometheus and automatically scales service replicas up or down to meet demand. Includes configurable cooldown periods to prevent flapping.

## Labels

```yaml
deploy:
  labels:
    swarmex.scaler.enabled: "true"       # Enable autoscaling
    swarmex.scaler.min: "2"              # Minimum replicas
    swarmex.scaler.max: "10"             # Maximum replicas
    swarmex.scaler.cpu-target: "70"      # Target CPU % to trigger scaling
    swarmex.scaler.ram-target: "80"      # Target RAM % to trigger scaling
    swarmex.scaler.cooldown: "60"        # Seconds between scale actions
```

## How It Works

1. Queries Prometheus for per-service CPU and RAM usage at regular intervals.
2. Compares current usage against configured targets.
3. Calculates desired replica count within min/max bounds.
4. Scales the service via Docker API if the cooldown period has elapsed.
5. Scales down when load decreases, respecting the same cooldown.

## Quick Start

```bash
docker service update \
  --label-add swarmex.scaler.enabled=true \
  --label-add swarmex.scaler.min=2 \
  --label-add swarmex.scaler.max=10 \
  --label-add swarmex.scaler.cpu-target=70 \
  my-app
```

## Verified

test-app scaled from 2→5 replicas under load, then back to 2 when load subsided.

## License

Apache-2.0
