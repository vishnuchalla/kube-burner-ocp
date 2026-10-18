// Copyright 2026 The Kube-burner Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package workloads

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	deadlineFactor        = 3
	deadlineGrace         = 5 * time.Minute
	defaultIdentitySecret = "maas-perf-identities"
	// Resource footprint of a single GuideLLM process. A pod driving K
	// identities runs K processes, so its requests scale with --keys-per-pod.
	perProcessCPUMillis = 250
	perProcessMemoryMi  = 256
)

// supportedLoadProfiles are the GuideLLM profile kinds this workload knows how
// to parameterize. constant and poisson are open loop (the level is a request
// rate); concurrent is closed loop (the level is a number of streams).
var supportedLoadProfiles = map[string]bool{
	"constant":   true,
	"poisson":    true,
	"concurrent": true,
}

// payloadPresets are named prompt/output token shapes. agentic approximates the
// coding-agent payload (~32KiB of context, long answer) and qa the short
// question/short answer end of a mixed workload.
var payloadPresets = map[string][2]int{
	"small":      {32, 64},
	"medium":     {256, 512},
	"large":      {1024, 1024},
	"very-large": {2048, 2048},
	"qa":         {64, 256},
	"agentic":    {8192, 2048},
}

// normalizeTargets validates which halves of the A/B matrix to run. Running
// baseline alone is useful when the gateway's auth is not yet usable.
func normalizeTargets(targets string) string {
	valid := map[string]bool{"baseline": true, "gateway": true}
	var out []string
	for _, t := range strings.Split(targets, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !valid[t] {
			log.Fatalf("Invalid --targets entry %q: supported values are baseline, gateway", t)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		log.Fatal("--targets must contain at least one of baseline, gateway")
	}
	return strings.Join(out, ",")
}

// normalizeLoadProfiles trims and validates the comma-separated profile list.
func normalizeLoadProfiles(loadProfiles string) string {
	var profiles []string
	for _, profile := range strings.Split(loadProfiles, ",") {
		profile = strings.TrimSpace(profile)
		if profile == "" {
			continue
		}
		if !supportedLoadProfiles[profile] {
			log.Fatalf("Invalid --load-profiles entry %q: supported kinds are constant, poisson, concurrent", profile)
		}
		profiles = append(profiles, profile)
	}
	if len(profiles) == 0 {
		log.Fatal("--load-profiles must contain at least one profile kind")
	}
	return strings.Join(profiles, ",")
}

// normalizePayloadSizes resolves every entry to a "label:promptTokens:outputTokens"
// triple. An entry is either a preset name or an inline "<prompt>:<output>" pair,
// so arbitrary shapes can be benchmarked without extending the preset table.
func normalizePayloadSizes(payloadSizes string) string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(payloadSizes, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		var label string
		var prompt, output int
		if preset, ok := payloadPresets[entry]; ok {
			label, prompt, output = entry, preset[0], preset[1]
		} else {
			parts := strings.Split(entry, ":")
			if len(parts) != 2 {
				log.Fatalf("Invalid --payload-sizes entry %q: expected a preset name (%s) or an inline <promptTokens>:<outputTokens> pair, e.g. 8192:2048",
					entry, strings.Join(presetNames(), ", "))
			}
			prompt = mustPositiveInt(parts[0], entry, "promptTokens")
			output = mustPositiveInt(parts[1], entry, "outputTokens")
			label = fmt.Sprintf("p%d-o%d", prompt, output)
		}
		if seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, fmt.Sprintf("%s:%d:%d", label, prompt, output))
	}
	if len(out) == 0 {
		log.Fatal("--payload-sizes must contain at least one entry")
	}
	return strings.Join(out, ",")
}

func presetNames() []string {
	names := make([]string, 0, len(payloadPresets))
	for _, n := range []string{"small", "medium", "large", "very-large", "qa", "agentic"} {
		if _, ok := payloadPresets[n]; ok {
			names = append(names, n)
		}
	}
	return names
}

func mustPositiveInt(raw, entry, field string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 {
		log.Fatalf("Invalid --payload-sizes entry %q: %s must be a positive integer", entry, field)
	}
	return value
}

// thinkTime is the parsed form of --think-time. GuideLLM models think time as a
// requeue delay between the turns of a conversation, so it only has an effect
// when there is more than one turn.
type thinkTime struct {
	enabled bool
	min     float64
	max     float64
	avg     float64
	stdev   float64
}

// parseThinkTime accepts "" (disabled) or a "min,max" duration pair. GuideLLM
// samples the delay from a clamped normal distribution rather than a uniform
// one, so the stdev is set to a quarter of the range: +/-2 sigma then spans the
// requested window before clamping.
func parseThinkTime(spec string) thinkTime {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return thinkTime{}
	}
	parts := strings.Split(spec, ",")
	if len(parts) != 2 {
		log.Fatalf("Invalid --think-time %q: expected a <min>,<max> duration pair, e.g. 15s,45s", spec)
	}
	minDelay, err := time.ParseDuration(strings.TrimSpace(parts[0]))
	if err != nil {
		log.Fatalf("Invalid --think-time minimum %q: %v", parts[0], err)
	}
	maxDelay, err := time.ParseDuration(strings.TrimSpace(parts[1]))
	if err != nil {
		log.Fatalf("Invalid --think-time maximum %q: %v", parts[1], err)
	}
	if minDelay < 0 || maxDelay <= 0 {
		log.Fatalf("Invalid --think-time %q: durations must be positive", spec)
	}
	if maxDelay < minDelay {
		log.Fatalf("Invalid --think-time %q: maximum must not be smaller than minimum", spec)
	}
	tt := thinkTime{
		enabled: true,
		min:     minDelay.Seconds(),
		max:     maxDelay.Seconds(),
	}
	tt.avg = (tt.min + tt.max) / 2
	tt.stdev = (tt.max - tt.min) / 4
	return tt
}

// autoTurns sizes a conversation so it spans the whole measurement window
// instead of finishing early and leaving the tenant idle.
func autoTurns(tt thinkTime, benchmarkDuration time.Duration) int {
	if !tt.enabled || tt.avg <= 0 {
		return 1
	}
	turns := int(benchmarkDuration.Seconds()/tt.avg) + 1
	if turns < 2 {
		turns = 2
	}
	return turns
}

// identityPods is the number of pods needed to drive the whole tenant pool when
// each pod runs keysPerPod GuideLLM processes.
func identityPods(identities, keysPerPod int) int {
	if identities < 1 {
		return 1
	}
	if keysPerPod < 1 {
		keysPerPod = 1
	}
	return (identities + keysPerPod - 1) / keysPerPod
}

// autoJobDeadline covers every sample batch, since activeDeadlineSeconds applies
// to the whole Job rather than a single pod.
func autoJobDeadline(samples, parallelism int, warmup, benchmarkDuration time.Duration) time.Duration {
	if samples < 1 {
		samples = 1
	}
	if parallelism < 1 {
		parallelism = 1
	}
	batches := (samples + parallelism - 1) / parallelism
	return time.Duration(batches)*(warmup+benchmarkDuration)*deadlineFactor + deadlineGrace
}

// NewMaasGatewayPerf holds MaaS Gateway performance benchmark workload
func NewMaasGatewayPerf(wh *workloads.WorkloadHelper) *cobra.Command {
	var gatewayHost, gatewayIP, simulatorHost, providers, payloadSizes, concurrencyLevels string
	var guidellmImage, loadProfiles, apiKeySecret string
	var meteringNamespace, tokenizer, targets, thinkTimeSpec string
	var benchmarkDuration, warmup, pause, jobDeadline time.Duration
	var samples, parallelism, identities, keysPerPod, turns, seedRequests int
	var metricsProfiles []string
	var rc int

	cmd := &cobra.Command{
		Use:   "maas-gateway-perf",
		Short: "Runs maas-gateway-perf workload",
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["GATEWAY_HOST"] = gatewayHost
			AdditionalVars["GATEWAY_IP"] = gatewayIP
			u, err := url.Parse(gatewayHost)
			if err != nil || u.Scheme == "" || u.Hostname() == "" {
				log.Fatalf("Invalid --gateway-host %q: expected a full URL including scheme and host, e.g. https://maas.apps.example.com", gatewayHost)
			}
			AdditionalVars["GATEWAY_HOSTNAME"] = u.Hostname()
			AdditionalVars["SIMULATOR_HOST"] = simulatorHost
			AdditionalVars["PROVIDERS"] = providers
			AdditionalVars["PAYLOAD_SPECS"] = normalizePayloadSizes(payloadSizes)
			AdditionalVars["CONCURRENCY_LEVELS"] = concurrencyLevels
			AdditionalVars["LOAD_PROFILES"] = normalizeLoadProfiles(loadProfiles)

			if keysPerPod < 1 {
				log.Fatalf("--keys-per-pod must be at least 1, got %d", keysPerPod)
			}

			// --identities mints its own pool, so it implies the Secret name and
			// sizes the Job to cover the whole pool concurrently: every sample is
			// then a full sweep of the pool rather than a single tenant.
			if identities > 0 {
				if meteringNamespace == "" {
					log.Fatal("--identities requires --metering-namespace: the namespace of the metering-service that mints the tenants")
				}
				if apiKeySecret == "" {
					apiKeySecret = defaultIdentitySecret
				}
				pods := identityPods(identities, keysPerPod)
				if parallelism < pods {
					log.Infof("Raising --parallelism to %d so all %d identities are active concurrently at %d keys per pod", pods, identities, keysPerPod)
					parallelism = pods
				}
			}
			completions := samples
			if identities > 0 {
				completions = samples * identityPods(identities, keysPerPod)
			}
			if apiKeySecret != "" && parallelism < 2 && keysPerPod < 2 {
				log.Warnf("--api-key-secret is set but --parallelism is %d and --keys-per-pod is 1: only one identity will be active at a time. Raise --parallelism or --keys-per-pod to simulate concurrent tenants.", parallelism)
			}

			tt := parseThinkTime(thinkTimeSpec)
			if tt.enabled && turns < 2 {
				turns = autoTurns(tt, benchmarkDuration)
				log.Infof("--think-time is set, raising --turns to %d so conversations span the %s measurement window", turns, benchmarkDuration)
			}
			if turns < 1 {
				turns = 1
			}
			if !tt.enabled && turns > 1 {
				log.Infof("--turns is %d with no --think-time: turns follow one another with no delay", turns)
			}
			AdditionalVars["THINK_TIME_ENABLED"] = tt.enabled
			AdditionalVars["THINK_TIME_AVG"] = strconv.FormatFloat(tt.avg, 'f', -1, 64)
			AdditionalVars["THINK_TIME_MIN"] = strconv.FormatFloat(tt.min, 'f', -1, 64)
			AdditionalVars["THINK_TIME_MAX"] = strconv.FormatFloat(tt.max, 'f', -1, 64)
			AdditionalVars["THINK_TIME_STDEV"] = strconv.FormatFloat(tt.stdev, 'f', -1, 64)
			AdditionalVars["TURNS"] = turns

			if seedRequests > 0 && apiKeySecret == "" {
				log.Fatal("--seed-requests-per-identity needs a tenant pool: pass --identities or --api-key-secret")
			}
			AdditionalVars["SEED_REQUESTS"] = seedRequests
			// Seeding sweeps the pool exactly once, so it uses one completion per
			// pod rather than the benchmark's samples-per-pod.
			AdditionalVars["SEED_COMPLETIONS"] = parallelism
			AdditionalVars["API_KEY_SECRET"] = apiKeySecret
			AdditionalVars["IDENTITIES"] = identities
			AdditionalVars["KEYS_PER_POD"] = keysPerPod
			AdditionalVars["POD_CPU_MILLIS"] = keysPerPod * perProcessCPUMillis
			AdditionalVars["POD_MEMORY_MI"] = keysPerPod * perProcessMemoryMi
			AdditionalVars["METERING_NAMESPACE"] = meteringNamespace
			AdditionalVars["COMPLETIONS"] = completions
			AdditionalVars["TOKENIZER"] = tokenizer
			AdditionalVars["TARGETS"] = normalizeTargets(targets)
			if jobDeadline <= 0 {
				jobDeadline = autoJobDeadline(completions, parallelism, warmup, benchmarkDuration)
			}
			log.Infof("Per-Job deadline (activeDeadlineSeconds): %s", jobDeadline)
			AdditionalVars["JOB_DEADLINE"] = int(jobDeadline.Seconds())
			AdditionalVars["BENCHMARK_DURATION"] = int(benchmarkDuration.Seconds())
			AdditionalVars["WARMUP"] = int(warmup.Seconds())
			AdditionalVars["GUIDELLM_IMAGE"] = guidellmImage
			AdditionalVars["SAMPLES"] = samples
			AdditionalVars["PARALLELISM"] = parallelism
			AdditionalVars["PAUSE"] = pause
			rc = RunWorkload(cmd, wh, "maas-gateway-perf.yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVar(&gatewayHost, "gateway-host", "", "Full gateway URL (e.g., https://maas.apps.example.com)")
	cmd.Flags().StringVar(&gatewayIP, "gateway-ip", "", "Gateway ClusterIP pinned into the pods' hostAliases, which bypasses the ingress router. Leave unset to resolve --gateway-host normally and exercise the edge (router, TLS termination) along with the gateway")
	cmd.Flags().StringVar(&simulatorHost, "simulator-host", "", "Simulator URL (e.g., http://llm-d-inference-sim.llm.svc.cluster.local:8000)")
	cmd.Flags().StringVar(&providers, "providers", "gpt-4o-openai,claude-sonnet-anthropic", "Comma-separated provider model names")
	cmd.Flags().StringVar(&payloadSizes, "payload-sizes", "small,medium", "Comma-separated payload shapes. Either a preset -- small(32/64), medium(256/512), large(1024/1024), very-large(2048/2048), qa(64/256), agentic(8192/2048) -- or an inline <promptTokens>:<outputTokens> pair such as 8192:2048")
	cmd.Flags().StringVar(&concurrencyLevels, "concurrency-levels", "8,32,64,128,512", "Comma-separated load levels; requests/sec for the constant and poisson profiles (open loop), concurrent streams for the concurrent profile (closed loop)")
	cmd.Flags().StringVar(&targets, "targets", "baseline,gateway", "Which halves of the A/B matrix to run: baseline (direct to simulator), gateway, or both")
	cmd.Flags().StringVar(&loadProfiles, "load-profiles", "constant", "Comma-separated GuideLLM profile kinds: constant (open loop, uniform arrivals), poisson (open loop, random arrivals), concurrent (closed loop, fixed streams)")
	cmd.Flags().StringVar(&apiKeySecret, "api-key-secret", "", "Pre-existing Secret with a keys.txt of 'user<TAB>key' rows; each indexed pod binds to a slice of the pool. Defaults to the ServiceAccount token when unset")
	cmd.Flags().IntVar(&identities, "identities", 0, "Mint this many MaaS tenants before the run and revoke them after; implies --api-key-secret "+defaultIdentitySecret+" and sizes --parallelism to cover the pool. 0 disables minting")
	cmd.Flags().IntVar(&keysPerPod, "keys-per-pod", 1, "Tenants driven by a single pod, each as its own GuideLLM process. Raising this trades pod count for per-pod CPU and memory, which is how pools of hundreds or thousands of tenants stay schedulable")
	cmd.Flags().StringVar(&meteringNamespace, "metering-namespace", "", "Namespace of the metering-service used to mint and revoke identities; required with --identities")
	cmd.Flags().StringVar(&thinkTimeSpec, "think-time", "", "Soak mode: pause for a delay sampled from this <min>,<max> window between the turns of a conversation, e.g. 15s,45s. Empty disables think time")
	cmd.Flags().IntVar(&turns, "turns", 1, "User turns per conversation. Values above 1 keep a tenant on one conversation instead of starting a fresh one each request; derived from --think-time and --benchmark-duration when think time is set")
	cmd.Flags().IntVar(&seedRequests, "seed-requests-per-identity", 0, "Send this many completions per tenant through the gateway before benchmarking, so quota and usage history exist in the metering DB. 0 skips seeding")
	cmd.Flags().StringVar(&tokenizer, "tokenizer", "gpt2", "Tokenizer for synthetic prompt generation; a HuggingFace model id, which the pods need egress to huggingface.co to fetch")
	cmd.Flags().DurationVar(&jobDeadline, "job-deadline", 0, "Hard per-Job deadline (activeDeadlineSeconds); 0 derives it from samples, warmup and benchmark-duration")
	cmd.Flags().DurationVar(&benchmarkDuration, "benchmark-duration", 90*time.Second, "Duration of each benchmark run")
	cmd.Flags().DurationVar(&warmup, "warmup", 30*time.Second, "Warmup period discarded from results")
	cmd.Flags().StringVar(&guidellmImage, "guidellm-image", "ghcr.io/cloud-bulldozer/guidellm-results-parser:v0.0.5", "GuideLLM container image with parser")
	cmd.Flags().IntVar(&samples, "samples", 3, "Number of benchmark samples (Job completions)")
	cmd.Flags().IntVar(&parallelism, "parallelism", 1, "Job parallelism for benchmark runs")
	cmd.Flags().DurationVar(&pause, "pause", 10*time.Second, "Pause after each benchmark job completes")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"maas-gateway-perf-metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("gateway-host")
	cmd.MarkFlagRequired("simulator-host")
	return cmd
}
