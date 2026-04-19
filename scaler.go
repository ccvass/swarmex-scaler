package scaler

import (
	"context"
	"encoding/json"
	"math"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"
)

const (
	labelEnabled  = "swarmex.scaler.enabled"
	labelMin      = "swarmex.scaler.min"
	labelMax      = "swarmex.scaler.max"
	labelCPU      = "swarmex.scaler.cpu-target"
	labelRAM      = "swarmex.scaler.ram-target"
	labelCooldown = "swarmex.scaler.cooldown"
)

// Config parsed from Docker service labels.
type Config struct {
	Min       uint64
	Max       uint64
	CPUTarget float64 // percentage 0-100
	RAMTarget float64
	Cooldown  time.Duration
}

type serviceState struct {
	config    Config
	name      string
	lastScale time.Time
}

// Scaler watches Docker services and auto-scales replicas based on Prometheus metrics.
type Scaler struct {
	docker        *client.Client
	prometheusURL string
	logger        *slog.Logger
	services      map[string]*serviceState
	mu            sync.Mutex
	httpClient    *http.Client
}

// New creates a Scaler.
func New(cli *client.Client, prometheusURL string, logger *slog.Logger) *Scaler {
	return &Scaler{
		docker:        cli,
		prometheusURL: prometheusURL,
		logger:        logger,
		services:      make(map[string]*serviceState),
		httpClient:    &http.Client{Timeout: 10 * time.Second},
	}
}

// HandleEvent processes Docker service events.
func (s *Scaler) HandleEvent(ctx context.Context, event events.Message) {
	if event.Type != events.ServiceEventType {
		return
	}
	switch event.Action {
	case events.ActionCreate, events.ActionUpdate:
		s.reconcileService(ctx, event.Actor.ID)
	case events.ActionRemove:
		s.mu.Lock()
		delete(s.services, event.Actor.ID)
		s.mu.Unlock()
	}
}

// RunLoop periodically evaluates scaling decisions.
func (s *Scaler) RunLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.evaluateAll(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Scaler) reconcileService(ctx context.Context, serviceID string) {
	svc, _, err := s.docker.ServiceInspectWithRaw(ctx, serviceID, types.ServiceInspectOptions{})
	if err != nil {
		return
	}
	labels := svc.Spec.Labels
	if labels[labelEnabled] != "true" {
		s.mu.Lock()
		delete(s.services, serviceID)
		s.mu.Unlock()
		return
	}

	cfg := parseScalerConfig(labels)
	s.mu.Lock()
	s.services[serviceID] = &serviceState{config: cfg, name: svc.Spec.Name}
	s.mu.Unlock()

	s.logger.Info("scaler watching service", "service", svc.Spec.Name, "min", cfg.Min, "max", cfg.Max, "cpu", cfg.CPUTarget, "ram", cfg.RAMTarget)
}

func (s *Scaler) evaluateAll(ctx context.Context) {
	s.mu.Lock()
	snapshot := make(map[string]*serviceState, len(s.services))
	for k, v := range s.services {
		snapshot[k] = v
	}
	s.mu.Unlock()

	for serviceID, state := range snapshot {
		s.evaluate(ctx, serviceID, state)
	}
}

func (s *Scaler) evaluate(ctx context.Context, serviceID string, state *serviceState) {
	if time.Since(state.lastScale) < state.config.Cooldown {
		return
	}

	svc, _, err := s.docker.ServiceInspectWithRaw(ctx, serviceID, types.ServiceInspectOptions{})
	if err != nil {
		return
	}
	if svc.Spec.Mode.Replicated == nil {
		return
	}
	current := *svc.Spec.Mode.Replicated.Replicas

	cpu := s.queryMetric(ctx, fmt.Sprintf(
		`avg(rate(container_cpu_usage_seconds_total{container_label_com_docker_swarm_service_name="%s"}[1m])) * 100`, state.name))
	ram := s.queryMetric(ctx, fmt.Sprintf(
		`avg(container_memory_usage_bytes{container_label_com_docker_swarm_service_name="%s",container_spec_memory_limit_bytes>0} / container_spec_memory_limit_bytes{container_label_com_docker_swarm_service_name="%s"}) * 100`, state.name, state.name))

	// If RAM query returns NaN/Inf (no memory limit set), ignore RAM for scaling
	if math.IsNaN(ram) || math.IsInf(ram, 0) {
		ram = 0
	}

	var desired uint64 = current
	if cpu > state.config.CPUTarget || ram > state.config.RAMTarget {
		desired = current + 1
	} else if cpu < state.config.CPUTarget*0.5 && ram < state.config.RAMTarget*0.5 && current > state.config.Min {
		desired = current - 1
	}

	if desired < state.config.Min {
		desired = state.config.Min
	}
	if desired > state.config.Max {
		desired = state.config.Max
	}
	if desired == current {
		return
	}

	s.logger.Info("scaling service",
		"service", state.name,
		"from", current, "to", desired,
		"cpu", fmt.Sprintf("%.1f%%", cpu),
		"ram", fmt.Sprintf("%.1f%%", ram),
	)

	svc.Spec.Mode.Replicated.Replicas = &desired
	_, err = s.docker.ServiceUpdate(ctx, serviceID, svc.Version, svc.Spec, types.ServiceUpdateOptions{})
	if err != nil {
		s.logger.Error("scale failed", "service", state.name, "error", err)
		return
	}

	s.mu.Lock()
	state.lastScale = time.Now()
	s.mu.Unlock()
}

func (s *Scaler) queryMetric(ctx context.Context, query string) float64 {
	u := fmt.Sprintf("%s/api/v1/query?query=%s", s.prometheusURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Data.Result) == 0 {
		return 0
	}
	if len(result.Data.Result[0].Value) < 2 {
		return 0
	}
	var valStr string
	if err := json.Unmarshal(result.Data.Result[0].Value[1], &valStr); err != nil {
		return 0
	}
	v, _ := strconv.ParseFloat(valStr, 64)
	return v
}

func parseScalerConfig(labels map[string]string) Config {
	cfg := Config{
		Min:       1,
		Max:       10,
		CPUTarget: 70,
		RAMTarget: 80,
		Cooldown:  60 * time.Second,
	}
	if v, err := strconv.ParseUint(labels[labelMin], 10, 64); err == nil {
		cfg.Min = v
	}
	if v, err := strconv.ParseUint(labels[labelMax], 10, 64); err == nil {
		cfg.Max = v
	}
	if v, err := strconv.ParseFloat(labels[labelCPU], 64); err == nil {
		cfg.CPUTarget = v
	}
	if v, err := strconv.ParseFloat(labels[labelRAM], 64); err == nil {
		cfg.RAMTarget = v
	}
	if d, err := time.ParseDuration(labels[labelCooldown]); err == nil {
		cfg.Cooldown = d
	}
	return cfg
}
